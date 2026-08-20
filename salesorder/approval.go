package salesorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in sales_order.sales_order_approval_status (AD-10).
const (
	approvalNone     = "none"     // no approvers configured for the current status
	approvalPending  = "pending"  // gated: awaiting the required sign-offs
	approvalApproved = "approved" // enough configured approvers have signed off
)

// ErrNotApprover is returned when a caller who is not a configured approver for
// the order's current status tries to approve it (AD-10). Maps to HTTP 403.
var ErrNotApprover = errors.New("you are not a configured approver for this order's current status")

// ErrApprovalRequired is returned when an order is asked to leave a status that
// still requires approval sign-off (AD-10). Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this order must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for an order
// whose current status has no configured approvers (AD-10). Maps to HTTP 409.
var ErrApprovalNotRequired = errors.New("this order's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for the
// SORD record type at a status. Zero ⇒ no approval gate at that status (AD-10).
func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM sales_order_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sales order approvers: %w", err)
	}
	return n, nil
}

// signOffCount returns how many distinct approvers have signed off on an order
// at a status.
func signOffCount(ctx context.Context, q workflow.Querier, orderInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM sales_order_approval
		WHERE sales_order_id = $1 AND record_status_id = $2`,
		orderInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sales order approvals: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether an employee is an active configured
// approver for the SORD record type at a status.
func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM sales_order_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check sales order approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on an order at its current status
// (AD-10). It requires the caller to be a configured approver for that status,
// is idempotent per (order, status, approver), and flips the header's
// approval_status to 'approved' once the sign-off count reaches the configured
// approver count. The order row is locked for the duration so concurrent
// approvals can't race the count. Returns the refreshed order.
//
// callerIsSuperAdmin lets a super admin (the wildcard-grant role) approve a
// gate they aren't personally configured on, skipping quorum entirely
// rather than counting as one more sign-off -- this is an override of the
// gate, not a substitute approver, so it's written to history as
// "approve_override" and always resolves straight to approved.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*Order, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve sales order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT sales_order_id, sales_order_status FROM sales_order
		WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sales order for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, sordRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
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
			UPDATE sales_order SET
				sales_order_approval_status = $2, sales_order_approved_by = $3, sales_order_updated_at = NOW()
			WHERE sales_order_id = $1`, internalID, approvalApproved, nullableInt(approverEmployeeID)); err != nil {
			return nil, fmt.Errorf("override approve sales order: %w", err)
		}
		writeHistory(ctx, tx, internalID, "approve_override", &curStatusID, &curStatusID, approverEmployeeID)
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit approve sales order: %w", err)
		}
		return Get(ctx, pool, uuid)
	}

	// Record the sign-off; a repeat sign-off by the same approver is a no-op.
	if _, err := tx.Exec(ctx, `
		INSERT INTO sales_order_approval (sales_order_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (sales_order_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record sales order approval: %w", err)
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
		UPDATE sales_order SET
			sales_order_approval_status = $2, sales_order_approved_by = $3, sales_order_updated_at = NOW()
		WHERE sales_order_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update sales order approval status: %w", err)
	}

	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve sales order: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// ApproverInfo names one configured approver for display (AD-10) —
// deliberately just id+name, not a full employee/user record, since the
// detail page only needs it to answer "who is this waiting on".
type ApproverInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ApprovalInfo tells the detail page whether an order is actually gated
// right now, who is configured to sign off on it, and whether the
// requesting caller can approve it (AD-10) -- so the UI can show a banner
// instead of a transition control that would just 409.
type ApprovalInfo struct {
	// Gated mirrors exactly the predicate store_transition.go's Transition
	// enforces (activeApproverCount > 0 && approvalStatus != approved) --
	// NOT the stored approvalStatus column alone, which goes stale the
	// moment an admin edits the approver list out from under an order
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

// GetApprovalInfo resolves ApprovalInfo for a sales order. Returns Gated:
// false without error once the order isn't currently gated.
func GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, uuid string, callerEmployeeID int, callerIsSuperAdmin bool) (ApprovalInfo, error) {
	var curStatusID int
	var approvalStatus string
	err := pool.QueryRow(ctx, `
		SELECT sales_order_status, sales_order_approval_status FROM sales_order
		WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL`, uuid).Scan(&curStatusID, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ApprovalInfo{}, ErrNotFound
	}
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("load sales order for approval info: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, pool, sordRecordTypeCode)
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("resolve SORD record type: %w", err)
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
		FROM sales_order_approver soa
		JOIN employee e ON e.employee_id = soa.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE soa.record_type_id = $1 AND soa.record_status_id = $2 AND soa.is_active
		ORDER BY 2`, recordTypeID, curStatusID)
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("load sales order approvers: %w", err)
	}
	defer rows.Close()
	var approvers []ApproverInfo
	for rows.Next() {
		var a ApproverInfo
		var id int
		if err := rows.Scan(&id, &a.Name); err != nil {
			return ApprovalInfo{}, fmt.Errorf("scan sales order approver: %w", err)
		}
		a.ID = fmt.Sprint(id)
		approvers = append(approvers, a)
	}
	if err := rows.Err(); err != nil {
		return ApprovalInfo{}, fmt.Errorf("iterate sales order approvers: %w", err)
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
