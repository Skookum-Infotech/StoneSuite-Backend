package creditmemo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// auditTable is the audit_logs.table_name stamped on every credit memo audit row.
const auditTable = "credit_memo"

// Audit actions written by this module (the controllers' action strings,
// unchanged now that the row is written inside the store transaction).
const (
	auditCreate     = "create"
	auditUpdate     = "update"
	auditDelete     = "delete"
	auditTransition = "transition"
	auditApply      = "apply"
	auditUnapply    = "unapply"
)

// AuditSnapshot is the audit_logs old/new value for a credit memo (nil-safe).
func AuditSnapshot(cm *CreditMemo) map[string]any {
	if cm == nil {
		return nil
	}
	return map[string]any{
		"id":               cm.ID,
		"number":           cm.Number,
		"statusCode":       cm.StatusCode,
		"customerId":       cm.Customer.ID,
		"ownerUserId":      cm.OwnerUserID,
		"grandTotal":       cm.GrandTotal,
		"appliedTotal":     cm.AppliedTotal,
		"unappliedAmount":  cm.UnappliedAmount,
		"lineCount":        len(cm.Lines),
		"applicationCount": len(cm.Applications),
		"customFields":     cm.CustomFields,
	}
}

// employeeExtra is the audit extra metadata naming the acting employee.
func employeeExtra(actorEmployeeID int) map[string]any {
	return map[string]any{"employee_id": actorEmployeeID}
}

// auditTx writes the credit memo audit row inside tx, as the LAST statement
// before commit. before is the pre-change snapshot (nil when the controller
// passed none); the after snapshot is re-read through tx unless action is
// delete. It is a no-op unless ctx carries a workflow.AuditRequest, and its
// error must make the caller roll back.
func auditTx(ctx context.Context, tx pgx.Tx, action, uuid string, before *CreditMemo, extra map[string]any) error {
	var after *CreditMemo
	if action != auditDelete {
		var err error
		after, err = getQ(ctx, tx, uuid)
		if err != nil {
			return fmt.Errorf("load credit memo for audit: %w", err)
		}
	}
	return workflow.LogAuditTx(ctx, tx, action, string(authz.ResourceCreditMemo), uuid, auditTable,
		AuditSnapshot(before), AuditSnapshot(after), extra)
}

// approvalConfig is moduleConfig plus the in-transaction audit hook for
// Approve/Reject, bound to the acting employee (the hook has no other way to
// learn who acted).
func approvalConfig(actorEmployeeID int) approvalchain.ModuleConfig {
	cfg := moduleConfig()
	cfg.AuditTx = func(ctx context.Context, tx pgx.Tx, _ int, uuid, action string) error {
		return auditTx(ctx, tx, action, uuid, nil, employeeExtra(actorEmployeeID))
	}
	return cfg
}
