// quote/approval.go
package quote

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in quote.quote_approval_status (AD-8).
const (
	approvalNone     = "none"     // no approvers configured for the current status
	approvalPending  = "pending"  // gated: awaiting the required sign-offs
	approvalApproved = "approved" // enough configured approvers have signed off
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the quote's current status tries to approve it (AD-8). Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this quote's current status")

// ErrApprovalRequired is returned when an quote is asked to leave a status
// that still requires approval sign-off (AD-8). Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this quote must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for an
// quote whose current status has no configured approvers (AD-8). Maps to 409.
var ErrApprovalNotRequired = errors.New("this quote's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for
// the QUOT record type at a status. Zero means no approval gate there (AD-8).
func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM quote_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quote approvers: %w", err)
	}
	return n, nil
}

// signOffCount returns how many distinct approvers have signed off on an
// quote at a status.
func signOffCount(ctx context.Context, q workflow.Querier, quoteInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM quote_approval
		WHERE quote_id = $1 AND record_status_id = $2`,
		quoteInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quote approvals: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether an employee is an active configured
// approver for the QUOT record type at a status.
func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM quote_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check quote approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on an quote at its current
// status (AD-8). Requires the caller to be a configured approver for that
// status, is idempotent per (quote, status, approver), and flips the
// header's approval_status to 'approved' once the sign-off count reaches the
// configured approver count.
//
// callerIsSuperAdmin lets a super admin (the wildcard-grant role) approve a
// gate they aren't personally configured on, skipping quorum entirely
// rather than counting as one more sign-off -- this is an override of the
// gate, not a substitute approver, so it's written to history as
// "approve_override" and always resolves straight to approved.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*Quote, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT quote_id, quote_status FROM quote
		WHERE quote_uuid = $1 AND quote_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, quotRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
	}

	required, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if required == 0 {
		return nil, ErrApprovalNotRequired
	}

	isApprover, err := isConfiguredApprover(ctx, tx, recordTypeID, curStatusID, approverEmployeeID)
	if err != nil {
		return nil, err
	}
	if !isApprover && !callerIsSuperAdmin {
		return nil, ErrNotApprover
	}

	if !isApprover && callerIsSuperAdmin {
		if _, err := tx.Exec(ctx, `
			UPDATE quote SET
				quote_approval_status = $2, quote_approved_by = $3, quote_updated_at = NOW()
			WHERE quote_id = $1`, internalID, approvalApproved, nullableInt(approverEmployeeID)); err != nil {
			return nil, fmt.Errorf("override approve quote: %w", err)
		}
		writeHistory(ctx, tx, internalID, "approve_override", &curStatusID, &curStatusID, approverEmployeeID)
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit approve quote: %w", err)
		}
		return Get(ctx, pool, uuid)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO quote_approval (quote_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (quote_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record quote approval: %w", err)
	}

	approved, err := signOffCount(ctx, tx, internalID, curStatusID)
	if err != nil {
		return nil, err
	}
	newStatus := approvalPending
	var approvedBy any
	if approved >= required {
		newStatus = approvalApproved
		approvedBy = approverEmployeeID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE quote SET
			quote_approval_status = $2, quote_approved_by = $3, quote_updated_at = NOW()
		WHERE quote_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update quote approval status: %w", err)
	}

	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve quote: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// ApproverInfo names one configured approver for display (AD-8) — deliberately
// just id+name, not a full employee/user record, since the detail page only
// needs it to answer "who is this waiting on".
type ApproverInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ApprovalInfo tells the detail page whether a quote is actually gated right
// now, who is configured to sign off on it, and whether the requesting
// caller can approve it (AD-8) -- so the UI can show a banner instead of a
// transition control that would just 409.
type ApprovalInfo struct {
	// Gated mirrors exactly the predicate store_transition.go's Transition
	// enforces (activeApproverCount > 0 && approvalStatus != approved) --
	// NOT the stored approvalStatus column alone, which goes stale the
	// moment an admin edits the approver list out from under a quote
	// already sitting in a gated status. If the configured approver list
	// for the current status is emptied, Gated flips to false immediately
	// (matching that Transition would no longer block), even though the
	// stored column still reads "pending" until the next transition.
	Gated bool `json:"gated"`
	// Approvers and CanApprove are only meaningful while Gated is true.
	Approvers  []ApproverInfo `json:"approvers"`
	CanApprove bool           `json:"canApprove"`
	// IsOverride is true when CanApprove is true only because the caller is
	// a super admin, not because they're on Approvers -- the UI uses this to
	// label the action as an override rather than an ordinary approval.
	IsOverride bool `json:"isOverride"`
}

// GetApprovalInfo resolves ApprovalInfo for a quote. Returns Gated: false
// without error once the quote isn't currently gated.
func GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, uuid string, callerEmployeeID int, callerIsSuperAdmin bool) (ApprovalInfo, error) {
	var curStatusID int
	var approvalStatus string
	err := pool.QueryRow(ctx, `
		SELECT quote_status, quote_approval_status FROM quote
		WHERE quote_uuid = $1 AND quote_deleted_at IS NULL`, uuid).Scan(&curStatusID, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalInfo{}, ErrNotFound
	}
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("load quote for approval info: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, pool, quotRecordTypeCode)
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("resolve QUOT record type: %w", err)
	}

	required, err := activeApproverCount(ctx, pool, recordTypeID, curStatusID)
	if err != nil {
		return ApprovalInfo{}, err
	}
	if required == 0 || approvalStatus == approvalApproved {
		return ApprovalInfo{Gated: false}, nil
	}

	rows, err := pool.Query(ctx, `
		SELECT e.employee_id, COALESCE(NULLIF(u.full_name,''), u.email)
		FROM quote_approver qa
		JOIN employee e ON e.employee_id = qa.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE qa.record_type_id = $1 AND qa.record_status_id = $2 AND qa.is_active
		ORDER BY 2`, recordTypeID, curStatusID)
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("load quote approvers: %w", err)
	}
	defer rows.Close()
	var approvers []ApproverInfo
	for rows.Next() {
		var a ApproverInfo
		var id int
		if err := rows.Scan(&id, &a.Name); err != nil {
			return ApprovalInfo{}, fmt.Errorf("scan quote approver: %w", err)
		}
		a.ID = fmt.Sprint(id)
		approvers = append(approvers, a)
	}
	if err := rows.Err(); err != nil {
		return ApprovalInfo{}, fmt.Errorf("iterate quote approvers: %w", err)
	}

	isApprover, err := isConfiguredApprover(ctx, pool, recordTypeID, curStatusID, callerEmployeeID)
	if err != nil {
		return ApprovalInfo{}, err
	}
	return ApprovalInfo{
		Gated:      true,
		Approvers:  approvers,
		CanApprove: isApprover || callerIsSuperAdmin,
		IsOverride: !isApprover && callerIsSuperAdmin,
	}, nil
}
