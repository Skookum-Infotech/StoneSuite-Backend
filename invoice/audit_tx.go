package invoice

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// auditTable is the audit_logs.table_name stamped on every invoice audit row.
const auditTable = "invoice"

// Audit actions written by this module (the controllers' action strings,
// unchanged now that the row is written inside the store transaction).
const (
	auditCreate     = "create"
	auditUpdate     = "update"
	auditDelete     = "delete"
	auditTransition = "transition"
	auditConvert    = "convert"
)

// AuditSnapshot is the audit_logs old/new value for an invoice (nil-safe).
func AuditSnapshot(i *Invoice) map[string]any {
	if i == nil {
		return nil
	}
	return map[string]any{
		"id":           i.ID,
		"number":       i.Number,
		"statusCode":   i.StatusCode,
		"customerId":   i.Customer.ID,
		"ownerUserId":  i.OwnerUserID,
		"grandTotal":   i.GrandTotal,
		"amountPaid":   i.AmountPaid,
		"balanceDue":   i.BalanceDue,
		"itemCount":    len(i.Items),
		"customFields": i.CustomFields,
	}
}

// employeeExtra is the audit extra metadata naming the acting employee.
func employeeExtra(actorEmployeeID int) map[string]any {
	return map[string]any{"employee_id": actorEmployeeID}
}

// auditTx writes the invoice audit row inside tx, as the LAST statement before
// commit. before is the pre-change snapshot (nil for create/transition/etc.);
// the after snapshot is re-read through tx unless action is delete. It is a
// no-op unless ctx carries a workflow.AuditRequest, and its error must make
// the caller roll back.
func auditTx(ctx context.Context, tx pgx.Tx, action, uuid string, before *Invoice, extra map[string]any) error {
	var after *Invoice
	if action != auditDelete {
		var err error
		after, err = getQ(ctx, tx, uuid)
		if err != nil {
			return fmt.Errorf("load invoice for audit: %w", err)
		}
	}
	return workflow.LogAuditTx(ctx, tx, action, string(authz.ResourceInvoice), uuid, auditTable,
		AuditSnapshot(before), AuditSnapshot(after), extra)
}

// AuditTxAs is auditTx for another module's store that changes an invoice in
// its own transaction (payment.QuickPay), with no before-snapshot.
func AuditTxAs(ctx context.Context, tx pgx.Tx, action, uuid string, actorEmployeeID int) error {
	return auditTx(ctx, tx, action, uuid, nil, employeeExtra(actorEmployeeID))
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
