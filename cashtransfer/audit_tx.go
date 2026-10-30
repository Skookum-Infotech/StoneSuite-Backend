// cashtransfer/audit_tx.go — the in-transaction audit row for cash transfer writes.
package cashtransfer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// auditTable is the audit_logs.table_name stamped on every cash transfer audit row.
const auditTable = "cash_transfer"

// Audit actions written by this module (the controllers' action strings,
// unchanged now that the row is written inside the store transaction).
const (
	auditCreate     = "create"
	auditUpdate     = "update"
	auditDelete     = "delete"
	auditTransition = "transition"
	auditPost       = "post"
	auditReverse    = "reverse"
)

// AuditSnapshot is the audit_logs old/new value for a cash transfer (nil-safe).
func AuditSnapshot(ct *CashTransfer) map[string]any {
	if ct == nil {
		return nil
	}
	return map[string]any{
		"id":            ct.ID,
		"number":        ct.Number,
		"statusCode":    ct.StatusCode,
		"fromAccountId": ct.FromAccount.ID,
		"toAccountId":   ct.ToAccount.ID,
		"amount":        ct.Amount,
		"ownerUserId":   ct.OwnerUserID,
		"customFields":  ct.CustomFields,
	}
}

// employeeExtra is the audit extra metadata naming the acting employee.
func employeeExtra(actorEmployeeID int) map[string]any {
	return map[string]any{"employee_id": actorEmployeeID}
}

// auditTx writes the cash transfer audit row inside tx, as the LAST statement
// before commit. before is the pre-change snapshot (nil unless the action
// reports one); the after snapshot is re-read through tx unless action is
// delete. It is a no-op unless ctx carries a workflow.AuditRequest, and its
// error must make the caller roll back.
func auditTx(ctx context.Context, tx pgx.Tx, action, uuid string, before *CashTransfer, actorEmployeeID int) error {
	var after *CashTransfer
	if action != auditDelete {
		var err error
		after, err = getQ(ctx, tx, uuid)
		if err != nil {
			return fmt.Errorf("load cash transfer for audit: %w", err)
		}
	}
	return workflow.LogAuditTx(ctx, tx, action, string(authz.ResourceCashTransfer), uuid, auditTable,
		AuditSnapshot(before), AuditSnapshot(after), employeeExtra(actorEmployeeID))
}
