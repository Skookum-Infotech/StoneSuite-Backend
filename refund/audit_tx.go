package refund

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// auditTable is the audit_logs.table_name stamped on every refund audit row.
const auditTable = "refund"

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

// AuditSnapshot is the audit_logs old/new value for a refund (nil-safe).
func AuditSnapshot(rf *Refund) map[string]any {
	if rf == nil {
		return nil
	}
	return map[string]any{
		"id":              rf.ID,
		"number":          rf.Number,
		"statusCode":      rf.StatusCode,
		"customerId":      rf.Customer.ID,
		"ownerUserId":     rf.OwnerUserID,
		"paymentId":       rf.PaymentID,
		"creditMemoId":    rf.CreditMemoID,
		"amount":          rf.Amount,
		"appliedTotal":    rf.AppliedTotal,
		"unappliedAmount": rf.UnappliedAmount,
		"customFields":    rf.CustomFields,
	}
}

// employeeExtra is the audit extra metadata naming the acting employee.
func employeeExtra(actorEmployeeID int) map[string]any {
	return map[string]any{"employee_id": actorEmployeeID}
}

// auditTx writes the refund audit row inside tx, as the LAST statement before
// commit. before is the pre-change snapshot (nil when the controller passed
// none); the after snapshot is re-read through tx unless action is delete. It
// is a no-op unless ctx carries a workflow.AuditRequest, and its error must
// make the caller roll back.
func auditTx(ctx context.Context, tx pgx.Tx, action, uuid string, before *Refund, extra map[string]any) error {
	var after *Refund
	if action != auditDelete {
		var err error
		after, err = getQ(ctx, tx, uuid)
		if err != nil {
			return fmt.Errorf("load refund for audit: %w", err)
		}
	}
	return workflow.LogAuditTx(ctx, tx, action, string(authz.ResourceRefund), uuid, auditTable,
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
