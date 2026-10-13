package vendorbill

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/docextract"
	"stonesuite-backend/workflow"
)

// dupLockKeyPrefix namespaces the advisory-lock key for the vendor invoice duplicate guard.
const dupLockKeyPrefix = "vb:"

// DuplicateDocumentError reports that a live vendor bill already exists for the
// same vendor and (normalized) vendor invoice number.
type DuplicateDocumentError struct {
	ExistingUUID   string
	ExistingNumber string
}

// Error implements error.
func (e *DuplicateDocumentError) Error() string {
	return fmt.Sprintf("a vendor bill with this invoice number already exists for this vendor (%s)", e.ExistingNumber)
}

// NormalizeDocNumber canonicalises a vendor invoice number for duplicate
// comparison. It delegates to docextract so the review screen's duplicate
// warning and this save-time guard always agree on what "the same invoice
// number" means.
func NormalizeDocNumber(s string) string {
	return docextract.NormalizeDocNumber(s)
}

// guardDuplicateInvoice serializes concurrent creates for the same vendor and
// invoice number with a transaction-scoped advisory lock, then looks for a live
// bill with the same normalized vendor invoice number. It returns the existing
// bill's (uuid, number) when one is found and allowDuplicate is true; a
// *DuplicateDocumentError when found and not allowed; and empty strings when
// there is no duplicate. excludeUUID (empty on create) skips the bill being
// edited so it never collides with itself. A blank invoice number is never guarded. Must run
// inside the create transaction so the lock is held until commit/rollback.
func guardDuplicateInvoice(ctx context.Context, q workflow.Querier, vendorInternalID int, invoiceNumber string, allowDuplicate bool, excludeUUID string) (dupUUID, dupNumber string, err error) {
	norm := NormalizeDocNumber(invoiceNumber)
	if strings.TrimSpace(invoiceNumber) == "" || norm == "" {
		return "", "", nil
	}
	key := fmt.Sprintf("%s%d:%s", dupLockKeyPrefix, vendorInternalID, norm)
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key); err != nil {
		return "", "", fmt.Errorf("lock duplicate invoice guard: %w", err)
	}
	rows, err := q.Query(ctx, `
		SELECT vendor_bill_uuid, COALESCE(vendor_bill_number, ''), vendor_bill_vendor_invoice_number
		FROM vendor_bill
		WHERE vendor_bill_vendor_id = $1
		  AND vendor_bill_vendor_invoice_number <> ''
		  AND vendor_bill_deleted_at IS NULL
		  AND regexp_replace(lower(vendor_bill_vendor_invoice_number), '[^a-z0-9]', '', 'g') LIKE '%' || $2 || '%'
		  AND ($3 = '' OR vendor_bill_uuid::text <> $3)
		ORDER BY vendor_bill_id`, vendorInternalID, norm, excludeUUID)
	if err != nil {
		return "", "", fmt.Errorf("scan existing invoice numbers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var u, n, inv string
		if err := rows.Scan(&u, &n, &inv); err != nil {
			return "", "", fmt.Errorf("scan existing invoice row: %w", err)
		}
		if NormalizeDocNumber(inv) != norm {
			continue
		}
		if !allowDuplicate {
			return "", "", &DuplicateDocumentError{ExistingUUID: u, ExistingNumber: n}
		}
		return u, n, nil
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("iterate existing invoice rows: %w", err)
	}
	return "", "", nil
}
