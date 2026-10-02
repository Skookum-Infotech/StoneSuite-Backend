package salesorder

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/docextract"
	"stonesuite-backend/workflow"
)

// dupLockKeyPrefix namespaces the advisory-lock key for the PO duplicate guard.
const dupLockKeyPrefix = "so:"

// DuplicateDocumentError reports that a live sales order already exists for the
// same customer and (normalized) PO number.
type DuplicateDocumentError struct {
	ExistingUUID   string
	ExistingNumber string
}

// Error implements error.
func (e *DuplicateDocumentError) Error() string {
	return fmt.Sprintf("a sales order with this PO number already exists for this customer (%s)", e.ExistingNumber)
}

// NormalizeDocNumber canonicalises a PO number for duplicate comparison. It
// delegates to docextract so the review screen's duplicate warning and this
// save-time guard always agree on what "the same PO number" means.
func NormalizeDocNumber(s string) string {
	return docextract.NormalizeDocNumber(s)
}

// guardDuplicatePO serializes concurrent creates for the same customer+PO with
// a transaction-scoped advisory lock, then looks for a live order with the same
// normalized PO. It returns the existing order (uuid, number) when one is found
// and allowDuplicate is true; a *DuplicateDocumentError when found and not
// allowed; and empty strings when there is no duplicate. Must run inside the
// create transaction so the lock is held until commit/rollback.
func guardDuplicatePO(ctx context.Context, q workflow.Querier, custInternalID int, po string, allowDuplicate bool) (dupUUID, dupNumber string, err error) {
	norm := NormalizeDocNumber(po)
	if strings.TrimSpace(po) == "" || norm == "" {
		return "", "", nil
	}
	key := fmt.Sprintf("%s%d:%s", dupLockKeyPrefix, custInternalID, norm)
	if _, err := q.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, key); err != nil {
		return "", "", fmt.Errorf("lock duplicate PO guard: %w", err)
	}
	rows, err := q.Query(ctx, `
		SELECT sales_order_uuid, sales_order_number, sales_order_po_number
		FROM sales_order
		WHERE sales_order_customer_id = $1
		  AND sales_order_po_number <> ''
		  AND sales_order_deleted_at IS NULL
		ORDER BY sales_order_id`, custInternalID)
	if err != nil {
		return "", "", fmt.Errorf("scan existing PO numbers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var u, n, p string
		if err := rows.Scan(&u, &n, &p); err != nil {
			return "", "", fmt.Errorf("scan existing PO row: %w", err)
		}
		if NormalizeDocNumber(p) != norm {
			continue
		}
		if !allowDuplicate {
			return "", "", &DuplicateDocumentError{ExistingUUID: u, ExistingNumber: n}
		}
		return u, n, nil
	}
	if err := rows.Err(); err != nil {
		return "", "", fmt.Errorf("iterate existing PO rows: %w", err)
	}
	return "", "", nil
}
