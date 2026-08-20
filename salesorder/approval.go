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
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*Order, error) {
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

	ok, err := isConfiguredApprover(ctx, tx, recordTypeID, curStatusID, approverEmployeeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotApprover
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

// ApprovalInfo tells the detail page who is waiting to sign off on an
// order's current status and whether the requesting caller is one of them
// (AD-10), so the UI can show a banner instead of a transition control that
// would just 409.
type ApprovalInfo struct {
	Approvers  []ApproverInfo `json:"approvers"`
	CanApprove bool           `json:"canApprove"`
}

// GetApprovalInfo resolves ApprovalInfo for a sales order. Returns the zero
// value (no approvers, cannot approve) without error once the order isn't
// currently gated -- callers only need this while ApprovalStatus == pending.
func GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, uuid string, callerEmployeeID int) (ApprovalInfo, error) {
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
	if approvalStatus != approvalPending {
		return ApprovalInfo{}, nil
	}

	recordTypeID, err := recordTypeIDByCode(ctx, pool, sordRecordTypeCode)
	if err != nil {
		return ApprovalInfo{}, fmt.Errorf("resolve SORD record type: %w", err)
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

	canApprove, err := isConfiguredApprover(ctx, pool, recordTypeID, curStatusID, callerEmployeeID)
	if err != nil {
		return ApprovalInfo{}, err
	}
	return ApprovalInfo{Approvers: approvers, CanApprove: canApprove}, nil
}
