package docextractjob

import (
	"context"
	"fmt"

	"stonesuite-backend/docextract"
)

// maxVendorDocCandidates bounds the per-vendor scan for duplicate documents.
const maxVendorDocCandidates = 200

// Vendor-document duplicate reasons.
const (
	reasonSameInvoice          = "Possible duplicate of %s (same vendor invoice number)"
	reasonSameInvoiceAndAmount = "Possible duplicate of %s (same vendor invoice number, same amount)"
	reasonSameReference        = "Possible duplicate of %s (same reference number)"
	reasonSameReferenceAmount  = "Possible duplicate of %s (same reference number, same total)"
)

// vendorDocCandidate is a live bill or purchase order of the vendor whose
// document number may equal the extracted one.
type vendorDocCandidate struct {
	uuid, number, docNumber, statusName, ownerUserID string
	totalCents                                       int64
}

// vendorBillFindings reports a live bill from the same vendor with the same
// normalized vendor invoice number (blocking at save time; see vendorbill).
func (c *Checker) vendorBillFindings(ctx context.Context, in DuplicateInput) ([]Duplicate, error) {
	key := docextract.NormalizeDocNumber(in.InvoiceNumber)
	if key == "" || !validUUID(in.VendorUUID) {
		return nil, nil
	}
	cands, err := c.loadVendorDocs(ctx, vendorBillCandidateSQL, in.VendorUUID, key, "vendor bills")
	if err != nil {
		return nil, err
	}
	return classifyVendorDocs(cands, key, in, false, DupSameInvoice, reasonSameInvoice, reasonSameInvoiceAndAmount), nil
}

// purchaseOrderFindings reports a live purchase order to the same vendor with
// the same normalized reference number (a warning only).
func (c *Checker) purchaseOrderFindings(ctx context.Context, in DuplicateInput) ([]Duplicate, error) {
	key := docextract.NormalizeDocNumber(in.PONumber)
	if key == "" || !validUUID(in.VendorUUID) {
		return nil, nil
	}
	cands, err := c.loadVendorDocs(ctx, purchaseOrderCandidateSQL, in.VendorUUID, key, "purchase orders")
	if err != nil {
		return nil, err
	}
	return classifyVendorDocs(cands, key, in, true, DupSameReference, reasonSameReference, reasonSameReferenceAmount), nil
}

// classifyVendorDocs is the pure matching rule: equal normalized document
// number, with an equal total as an extra signal in the reason. With matchOwn
// (purchase orders) the candidate's own number, minus our "PORD" prefix, also
// counts, since a vendor may quote the number we issued.
func classifyVendorDocs(cands []vendorDocCandidate, key string, in DuplicateInput, matchOwn bool, kind, reason, reasonWithTotal string) []Duplicate {
	var out []Duplicate
	for _, d := range cands {
		if docextract.NormalizeDocNumber(d.docNumber) != key && (!matchOwn || poNumberKey(d.number) != key) {
			continue
		}
		text := fmt.Sprintf(reason, d.number)
		if in.HasTotal && d.totalCents == in.TotalCents {
			text = fmt.Sprintf(reasonWithTotal, d.number)
		}
		out = append(out, Duplicate{Kind: kind, RecordUUID: d.uuid, Number: d.number, Status: d.statusName,
			Reason: text, OwnerUserID: d.ownerUserID})
	}
	return out
}

// Candidate queries. $1 is the vendor uuid, $2 the normalized key, $3 the cap.
// The sanitized-contains test is a superset of the exact normalized match
// (leading zeros and labels only shorten the key); Go applies the exact rule.
const (
	vendorBillCandidateSQL = `
		SELECT d.vendor_bill_uuid::text, COALESCE(d.vendor_bill_number, ''), d.vendor_bill_vendor_invoice_number,
		       ROUND(d.vendor_bill_grand_total * 100)::bigint, rs.record_status_name, COALESCE(ou.id::text, '')
		FROM vendor_bill d
		JOIN vendor v ON v.vendor_id = d.vendor_bill_vendor_id
		JOIN lkp_record_status rs ON rs.record_status_id = d.vendor_bill_status
		LEFT JOIN employee oe ON oe.employee_id = d.vendor_bill_owner_id
		LEFT JOIN users ou ON ou.id = oe.employee_user_id
		WHERE d.vendor_bill_deleted_at IS NULL AND v.vendor_uuid = $1::uuid
		  AND regexp_replace(lower(d.vendor_bill_vendor_invoice_number), '[^a-z0-9]', '', 'g') LIKE '%' || $2 || '%'
		ORDER BY d.vendor_bill_id DESC LIMIT $3`
	purchaseOrderCandidateSQL = `
		SELECT d.purchase_order_uuid::text, COALESCE(d.purchase_order_number, ''), d.purchase_order_reference_number,
		       ROUND(d.purchase_order_grand_total * 100)::bigint, rs.record_status_name, COALESCE(ou.id::text, '')
		FROM purchase_order d
		JOIN vendor v ON v.vendor_id = d.purchase_order_vendor_id
		JOIN lkp_record_status rs ON rs.record_status_id = d.purchase_order_status
		LEFT JOIN employee oe ON oe.employee_id = d.purchase_order_owner_id
		LEFT JOIN users ou ON ou.id = oe.employee_user_id
		WHERE d.purchase_order_deleted_at IS NULL AND v.vendor_uuid = $1::uuid
		  AND (regexp_replace(lower(d.purchase_order_reference_number), '[^a-z0-9]', '', 'g') LIKE '%' || $2 || '%'
		    OR regexp_replace(lower(COALESCE(d.purchase_order_number, '')), '[^a-z0-9]', '', 'g') LIKE '%' || $2 || '%')
		ORDER BY d.purchase_order_id DESC LIMIT $3`
)

// loadVendorDocs runs one of the candidate queries; what names the documents
// for error messages.
func (c *Checker) loadVendorDocs(ctx context.Context, sql, vendorUUID, key, what string) ([]vendorDocCandidate, error) {
	rows, err := c.db.Query(ctx, sql, vendorUUID, key, maxVendorDocCandidates)
	if err != nil {
		return nil, fmt.Errorf("query %s for duplicates: %w", what, err)
	}
	defer rows.Close()
	var out []vendorDocCandidate
	for rows.Next() {
		var d vendorDocCandidate
		if err := rows.Scan(&d.uuid, &d.number, &d.docNumber, &d.totalCents, &d.statusName, &d.ownerUserID); err != nil {
			return nil, fmt.Errorf("scan %s candidate: %w", what, err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s candidates: %w", what, err)
	}
	return out, nil
}
