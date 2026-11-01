// vendorbill/audit_tx.go — the in-transaction audit row for vendor bill writes.
package vendorbill

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/workflow"
)

// auditResource is both the audit_logs resource and table_name for vendor bills.
const auditResource = "vendor_bill"

// Audit actions written by this module (the controllers' action strings,
// unchanged now that the row is written inside the store transaction).
const (
	auditCreate         = "create"
	auditUpdate         = "update"
	auditDelete         = "delete"
	auditTransition     = "transition"
	auditConvert        = "convert"
	auditRecordPayment  = "record_payment"
	auditUnapplyPayment = "unapply_payment"
)

// AuditSnapshot is the audit_logs old/new value for a vendor bill (nil-safe).
func AuditSnapshot(b *VendorBill) map[string]any {
	if b == nil {
		return nil
	}
	return map[string]any{
		"id":               b.ID,
		"vendorBillNumber": b.Number,
		"status":           b.StatusName,
		"vendorId":         b.Vendor.ID,
		"grandTotal":       b.GrandTotal,
		"balanceDue":       b.BalanceDue,
	}
}

// duplicateExtra is the audit extra metadata for a create/update that
// overrode the duplicate guard, or nil otherwise.
func duplicateExtra(dupUUID string) map[string]any {
	if dupUUID == "" {
		return nil
	}
	return map[string]any{"duplicate_override": true, "existing_uuid": dupUUID}
}

// auditTx writes the vendor bill audit row inside tx, as the LAST statement
// before commit. before is the pre-change snapshot (nil unless the action
// reports one); the after snapshot is re-read through tx unless action is
// delete. It is a no-op unless ctx carries a workflow.AuditRequest, and its
// error must make the caller roll back.
func auditTx(ctx context.Context, tx pgx.Tx, action, uuid string, before *VendorBill, extra map[string]any) error {
	var after *VendorBill
	if action != auditDelete {
		var err error
		after, err = getQ(ctx, tx, uuid)
		if err != nil {
			return fmt.Errorf("load vendor bill for audit: %w", err)
		}
	}
	return workflow.LogAuditTx(ctx, tx, action, auditResource, uuid, auditResource,
		AuditSnapshot(before), AuditSnapshot(after), extra)
}

// approvalConfig is moduleConfig plus the in-transaction audit hook for
// Approve/Reject.
func approvalConfig() approvalchain.ModuleConfig {
	cfg := moduleConfig()
	cfg.AuditTx = func(ctx context.Context, tx pgx.Tx, _ int, uuid, action string) error {
		return auditTx(ctx, tx, action, uuid, nil, nil)
	}
	return cfg
}
