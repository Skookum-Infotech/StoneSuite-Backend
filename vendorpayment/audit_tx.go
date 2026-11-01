// vendorpayment/audit_tx.go — the in-transaction audit row for vendor payment writes.
package vendorpayment

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// auditTable is the audit_logs.table_name stamped on every vendor payment audit row.
const auditTable = "vendor_payment"

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

// AuditSnapshot is the audit_logs old/new value for a vendor payment (nil-safe).
func AuditSnapshot(p *VendorPayment) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":              p.ID,
		"number":          p.Number,
		"statusCode":      p.StatusCode,
		"vendorId":        p.Vendor.ID,
		"ownerUserId":     p.OwnerUserID,
		"amount":          p.Amount,
		"appliedTotal":    p.AppliedTotal,
		"unappliedAmount": p.UnappliedAmount,
		"customFields":    p.CustomFields,
	}
}

// employeeExtra is the audit extra metadata naming the acting employee.
func employeeExtra(actorEmployeeID int) map[string]any {
	return map[string]any{"employee_id": actorEmployeeID}
}

// auditTx writes the vendor payment audit row inside tx, as the LAST
// statement before commit. before is the pre-change snapshot (nil unless the
// action reports one); the after snapshot is re-read through tx unless action
// is delete. It is a no-op unless ctx carries a workflow.AuditRequest, and its
// error must make the caller roll back.
func auditTx(ctx context.Context, tx pgx.Tx, action, uuid string, before *VendorPayment, extra map[string]any) error {
	var after *VendorPayment
	if action != auditDelete {
		var err error
		after, err = getQ(ctx, tx, uuid)
		if err != nil {
			return fmt.Errorf("load vendor payment for audit: %w", err)
		}
	}
	return workflow.LogAuditTx(ctx, tx, action, string(authz.ResourceVendorPayment), uuid, auditTable,
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
