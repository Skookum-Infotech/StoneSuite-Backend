package employee

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ErrNotApprover maps to HTTP 403.
var ErrNotApprover = errors.New("you are not a configured approver for this employee's current status")

// ErrApprovalRequired maps to HTTP 409.
var ErrApprovalRequired = errors.New("this employee must be approved before they can be made active")

// ErrApprovalNotRequired maps to HTTP 409.
var ErrApprovalNotRequired = errors.New("this employee's current status does not require approval")

// moduleConfig resolves the shared approvalchain.ModuleConfig for Employee
// (workflows.key "employee"). A missing entry is a programming error, caught by
// the registry test.
func moduleConfig() approvalchain.ModuleConfig {
	cfg, ok := approvalchain.ForWorkflowKey("employee")
	if !ok {
		panic("approvalchain: \"employee\" is not registered")
	}
	return cfg
}

// approvalConfig is moduleConfig plus the in-transaction hook that makes an
// approved employee usable. The engine moves the record to Active when the last
// approver signs (or a super admin overrides); this hook runs in that same
// transaction, so the employee can never be Active yet unusable, or the reverse.
func approvalConfig() approvalchain.ModuleConfig {
	cfg := moduleConfig()
	cfg.AuditTx = func(ctx context.Context, tx pgx.Tx, internalID int, _, action string) error {
		if action != "approve" {
			return nil
		}
		return activateApprovedTx(ctx, tx, internalID)
	}
	return cfg
}

// activateApprovedTx runs after an approver signs. A sign-off short of quorum
// leaves the employee Pending and does nothing; the one that completes the
// round has already moved it to Active, so it is switched on and given its
// Vendor record.
func activateApprovedTx(ctx context.Context, tx pgx.Tx, internalID int) error {
	var statusCode string
	var approverID *int
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(rs.record_status_code, ''), e.employee_approved_by
		FROM employee e
		LEFT JOIN lkp_record_status rs ON rs.record_status_id = e.employee_status
		WHERE e.employee_id = $1`, internalID).Scan(&statusCode, &approverID)
	if err != nil {
		return fmt.Errorf("load approved employee: %w", err)
	}
	if statusCode != statusActive {
		return nil
	}
	actor := 0
	if approverID != nil {
		actor = *approverID
	}
	if _, err := tx.Exec(ctx, `UPDATE employee SET employee_is_active = TRUE WHERE employee_id = $1`, internalID); err != nil {
		return fmt.Errorf("activate approved employee: %w", err)
	}
	return syncVendorTx(ctx, tx, internalID, actor)
}

// Approve records one approver's sign-off at the Pending Approval gate via the
// shared engine. Once every configured approver has signed -- or a super admin
// overrides -- the employee becomes Active and gets its Vendor record.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int, callerIsSuperAdmin bool) (*Employee, error) {
	_, err := approvalchain.Approve(ctx, pool, approvalConfig(), uuid, approverEmployeeID, callerIsSuperAdmin)
	if err := mapApprovalErr(err); err != nil {
		return nil, err
	}
	return Get(ctx, pool, uuid)
}

// Reject vetoes a pending employee on behalf of a configured approver (or a
// super admin): it stays Pending, flagged rejected with the reason kept, until
// someone edits it, which sends it back to the approvers.
func Reject(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int, callerIsSuperAdmin bool, reason string) (*Employee, error) {
	_, err := approvalchain.Reject(ctx, pool, approvalConfig(), uuid, actorEmployeeID, callerIsSuperAdmin, reason)
	if err := mapApprovalErr(err); err != nil {
		return nil, err
	}
	return Get(ctx, pool, uuid)
}

// GetApprovalInfo resolves who is configured to sign off on an employee's
// current gate, who already has, and whether the caller can approve or reject.
func GetApprovalInfo(ctx context.Context, pool *pgxpool.Pool, uuid string, callerEmployeeID int, callerIsSuperAdmin bool) (approvalchain.ApprovalInfo, error) {
	info, err := approvalchain.GetInfo(ctx, pool, moduleConfig(), uuid, callerEmployeeID, callerIsSuperAdmin)
	if errors.Is(err, approvalchain.ErrNotFound) {
		return approvalchain.ApprovalInfo{}, ErrNotFound
	}
	return info, err
}

// mapApprovalErr turns the engine's sentinel errors into this package's own,
// and passes every other error (including nil) through unchanged.
func mapApprovalErr(err error) error {
	switch {
	case errors.Is(err, approvalchain.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, approvalchain.ErrNotApprover):
		return ErrNotApprover
	case errors.Is(err, approvalchain.ErrApprovalNotRequired):
		return ErrApprovalNotRequired
	default:
		return err
	}
}
