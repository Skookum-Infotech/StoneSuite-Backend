// vendorcredit/store_update.go
package vendorcredit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Update edits a vendor credit header. There is no non-monetary/monetary
// split to preserve, unlike creditmemo.Update: a vendor credit has no lines,
// so the whole record is Draft-only (spec §8).
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in UpdateVendorCreditInput, actorEmployeeID int) (*VendorCredit, error) {
	internalID, statusCode, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if statusCode != draftStatusCode {
		return nil, ClientError{Msg: "Cannot edit a " + statusCode + " vendor credit; only Draft credits can be edited."}
	}
	if in.Amount != nil && *in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be greater than zero."}
	}

	// The column is NOT NULL DEFAULT '{}'. A nil Go map encodes as SQL NULL, so
	// every PATCH that omits customFields would 500 without this guard.
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	// Load the live applied_total so unapplied_amount is derived, not assumed.
	// It is always 0 at DRFT per the transition map, but read it defensively.
	var appliedTotal float64
	if err := pool.QueryRow(ctx,
		`SELECT vendor_credit_applied_total FROM vendor_credit WHERE vendor_credit_id = $1`, internalID,
	).Scan(&appliedTotal); err != nil {
		return nil, fmt.Errorf("load vendor credit applied total: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update vendor credit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cv := []colVal{
		{"vendor_credit_reference_number", in.ReferenceNumber, ""},
		{"vendor_credit_reason", in.Reason, ""},
		{"vendor_credit_memo", in.Memo, ""},
		{"vendor_credit_internal_notes", in.InternalNotes, ""},
		{"vendor_credit_custom_fields", custom, ""},
		{"vendor_credit_updated_by", nullableInt(actorEmployeeID), ""},
	}

	extraSets := []string{
		"vendor_credit_date = COALESCE(NULLIF($1,'')::date, vendor_credit_date)",
		"vendor_credit_owner_id = COALESCE($2, vendor_credit_owner_id)",
		"vendor_credit_updated_at = NOW()",
		"vendor_credit_record_version = vendor_credit_record_version + 1",
	}
	leadingArgs := []any{in.CreditDate, in.OwnerEmployeeID}

	if in.Amount != nil {
		extraSets = append(extraSets,
			fmt.Sprintf("vendor_credit_grand_total = $%d", len(leadingArgs)+1),
			fmt.Sprintf("vendor_credit_unapplied_amount = $%d", len(leadingArgs)+2))
		leadingArgs = append(leadingArgs, *in.Amount, *in.Amount-appliedTotal)
	}

	updateSQL, updateArgs := buildUpdateSet("vendor_credit", leadingArgs, cv, extraSets,
		fmt.Sprintf("vendor_credit_id = $%d", len(leadingArgs)+len(cv)+1))
	updateArgs = append(updateArgs, internalID)
	if _, err := tx.Exec(ctx, updateSQL, updateArgs...); err != nil {
		return nil, fmt.Errorf("update vendor credit: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_credit_history (vendor_credit_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT vendor_credit_id, vendor_credit_status, vendor_credit_status, 'update', $2
		FROM vendor_credit WHERE vendor_credit_id = $1`,
		internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor credit update history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update vendor credit: %w", err)
	}
	return Get(ctx, pool, id)
}

// SoftDelete marks a vendor credit deleted (paired deleted_at/deleted_by).
// Blocked while any live vendor_credit_application references it, so every
// visible ledger row's parent credit stays resolvable.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	internalID, _, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return err
	}
	var liveApplications int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vendor_credit_application WHERE vendor_credit_id = $1 AND application_deleted_at IS NULL`,
		internalID).Scan(&liveApplications); err != nil {
		return fmt.Errorf("count live vendor credit applications: %w", err)
	}
	if liveApplications > 0 {
		return ClientError{Msg: "Cannot delete a vendor credit with live applications; reverse them first."}
	}

	tag, err := pool.Exec(ctx, `
		UPDATE vendor_credit
		SET vendor_credit_deleted_at = NOW(), vendor_credit_deleted_by = $1
		WHERE vendor_credit_uuid = $2 AND vendor_credit_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), id)
	if err != nil {
		return fmt.Errorf("delete vendor credit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
