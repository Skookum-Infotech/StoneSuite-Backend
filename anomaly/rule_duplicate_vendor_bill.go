package anomaly

import (
	"context"
	"fmt"
	"time"

	"stonesuite-backend/approvalchain"
)

// vendorBillWorkflowKey is the approvalchain registry key for vendor bills.
const vendorBillWorkflowKey = "vendor_bill"

// DuplicateVendorBill flags a vendor bill with the same vendor, the same
// vendor-side invoice number and the same grand total as an earlier live bill:
// the classic way a supplier gets paid twice. The newer bill is the finding.
// It goes to that bill's owner and the vendor-bill approvers, who can still
// stop it, and not to super admins, who see everything on the Anomalies page.
type DuplicateVendorBill struct{}

// Key implements Rule.
func (DuplicateVendorBill) Key() string { return RuleDuplicateVendorBill }

type duplicateBill struct {
	uuid, number, earlierNumber string
	ownerIdentity               *string
}

// Detect implements Rule.
func (DuplicateVendorBill) Detect(ctx context.Context, q Querier, _ time.Time) ([]Finding, error) {
	bills, err := duplicateBills(ctx, q)
	if err != nil || len(bills) == 0 {
		return nil, err
	}
	cfg, ok := approvalchain.ForWorkflowKey(vendorBillWorkflowKey)
	if !ok {
		return nil, fmt.Errorf("duplicate vendor bill scan: %q is not in the approval registry", vendorBillWorkflowKey)
	}
	approvers, err := liveApproverIdentities(ctx, q, cfg)
	if err != nil {
		return nil, fmt.Errorf("duplicate vendor bill scan: %w", err)
	}
	out := make([]Finding, 0, len(bills))
	for _, b := range bills {
		audience := append([]string(nil), approvers...)
		if b.ownerIdentity != nil {
			audience = append(audience, *b.ownerIdentity)
		}
		out = append(out, Finding{
			RuleKey:    RuleDuplicateVendorBill,
			SubjectKey: b.uuid,
			Severity:   SeverityWarning,
			Resource:   cfg.Resource,
			RecordUUID: b.uuid,
			Title:      duplicateBillTitle(b.number),
			Detail: fmt.Sprintf("Same vendor, vendor invoice number and total as %s. "+
				"Check it before it is approved or paid.", b.earlierNumber),
			Audience: Audience{IdentityIDs: audience},
		})
	}
	return out, nil
}

// duplicateBills returns live bills that repeat an earlier live bill's vendor,
// vendor invoice number (case and spacing ignored) and grand total, skipping
// bills already reported.
func duplicateBills(ctx context.Context, q Querier) ([]duplicateBill, error) {
	rows, err := q.Query(ctx, `
		SELECT b.vendor_bill_uuid::text, COALESCE(b.vendor_bill_number, ''), COALESCE(d.vendor_bill_number, ''), uo.identity_id::text
		FROM vendor_bill b
		JOIN LATERAL (
		    SELECT o.vendor_bill_number
		    FROM vendor_bill o
		    WHERE o.vendor_bill_vendor_id = b.vendor_bill_vendor_id
		      AND o.vendor_bill_id < b.vendor_bill_id
		      AND o.vendor_bill_deleted_at IS NULL
		      AND lower(btrim(o.vendor_bill_vendor_invoice_number)) = lower(btrim(b.vendor_bill_vendor_invoice_number))
		      AND o.vendor_bill_grand_total = b.vendor_bill_grand_total
		    ORDER BY o.vendor_bill_id
		    LIMIT 1
		) d ON TRUE
		LEFT JOIN employee eo ON eo.employee_id = b.vendor_bill_owner_id
		LEFT JOIN users uo ON uo.id = eo.employee_user_id
		WHERE b.vendor_bill_deleted_at IS NULL
		  AND btrim(b.vendor_bill_vendor_invoice_number) <> ''
		  AND NOT EXISTS (
		      SELECT 1 FROM anomaly a
		      WHERE a.anomaly_rule_key = $1 AND a.anomaly_subject_key = b.vendor_bill_uuid::text)
		ORDER BY b.vendor_bill_id DESC
		LIMIT $2`, RuleDuplicateVendorBill, MaxFindingsPerRule)
	if err != nil {
		return nil, fmt.Errorf("duplicate vendor bill scan: %w", err)
	}
	defer rows.Close()

	var out []duplicateBill
	for rows.Next() {
		var b duplicateBill
		if err := rows.Scan(&b.uuid, &b.number, &b.earlierNumber, &b.ownerIdentity); err != nil {
			return nil, fmt.Errorf("duplicate vendor bill scan: scan: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("duplicate vendor bill scan: iterate: %w", err)
	}
	return out, nil
}

// liveApproverIdentities lists the identity ids of the module's active
// approvers across all of its gates, skipping deactivated users and deleted
// employees (approvalchain.LiveApproverJoin).
func liveApproverIdentities(ctx context.Context, q Querier, cfg approvalchain.ModuleConfig) ([]string, error) {
	rows, err := q.Query(ctx, fmt.Sprintf(`
		SELECT DISTINCT u.identity_id::text
		FROM %s ea %s
		WHERE ea.is_active`, cfg.ApproverTable, approvalchain.LiveApproverJoin))
	if err != nil {
		return nil, fmt.Errorf("load approvers from %s: %w", cfg.ApproverTable, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan approver: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approvers: %w", err)
	}
	return out, nil
}

// duplicateBillTitle names the bill, falling back to a generic label when it
// has no number.
func duplicateBillTitle(number string) string {
	if number == "" {
		return "Possible duplicate vendor bill"
	}
	return fmt.Sprintf("Possible duplicate vendor bill %s", number)
}
