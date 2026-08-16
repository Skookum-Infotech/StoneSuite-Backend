// vendorcredit/store_create.go
package vendorcredit

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// orNow returns the given "yyyy-mm-dd" date string, or "now" when blank, for
// use with a COALESCE(NULLIF($n, empty string)::date, CURRENT_DATE)-style bind.
func orNow(d string) string {
	if d == "" {
		return "now"
	}
	return d
}

// Create inserts a new vendor credit header inside one transaction: snapshots
// the vendor id + name (AD-12, rejecting an inactive vendor per AD-8),
// validates custom fields, assigns the VCR number post-insert, and starts the
// credit at DRFT. There are no lines (AD-1), so this is meaningfully shorter
// than creditmemo.Create/vendorbill.Create.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateVendorCreditInput, actorEmployeeID int) (*VendorCredit, error) {
	if strings.TrimSpace(in.VendorUUID) == "" {
		return nil, ClientError{Msg: "A vendor is required."}
	}
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be greater than zero."}
	}

	vendorInternalID, vendorName, err := activeVendorSnapshot(ctx, pool, in.VendorUUID)
	if err != nil {
		return nil, err
	}

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, pool, vcrdRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VCRD record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, pool, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create vendor credit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"vendor_credit_status", draftStatusID, ""},
		{"vendor_credit_vendor_id", vendorInternalID, ""},
		{"vendor_credit_vendor_name", vendorName, ""},
		{"vendor_credit_reference_number", in.ReferenceNumber, ""},
		{"vendor_credit_date", orNow(in.CreditDate), "::date"},
		{"vendor_credit_reason", in.Reason, ""},
		{"vendor_credit_memo", in.Memo, ""},
		{"vendor_credit_internal_notes", in.InternalNotes, ""},
		{"vendor_credit_owner_id", nullableInt(ownerEmployeeID), ""},
		{"vendor_credit_grand_total", in.Amount, ""},
		{"vendor_credit_applied_total", 0.0, ""},
		{"vendor_credit_unapplied_amount", in.Amount, ""},
		{"vendor_credit_custom_fields", custom, ""},
		{"vendor_credit_created_by", nullableInt(actorEmployeeID), ""},
	}

	insertSQL, insertArgs := buildInsert("vendor_credit", cv, "vendor_credit_id, vendor_credit_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "The referenced vendor does not exist."}
		}
		return nil, fmt.Errorf("insert vendor credit: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE vendor_credit SET vendor_credit_number = $1 WHERE vendor_credit_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set vendor credit number: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_credit_history (vendor_credit_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, internalID, draftStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor credit create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create vendor credit: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
