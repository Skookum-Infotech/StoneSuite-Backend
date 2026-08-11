package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func internalIDAndStatusByUUID(ctx context.Context, pool *pgxpool.Pool, id string) (int, string, error) {
	var internalID int
	var statusCode string
	err := pool.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, rs.record_status_code
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL`, id).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("resolve vendor bill: %w", err)
	}
	return internalID, statusCode, nil
}

// Update edits non-monetary header fields. Rejected (ClientError) unless the
// bill is still in DRFT — grand_total/amount_paid/balance_due are never
// touched here; the rollups are the sole domain of RecomputeBalance.
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in UpdateVendorBillInput, actorEmployeeID int) (*VendorBill, error) {
	internalID, statusCode, err := internalIDAndStatusByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if statusCode != "DRFT" {
		return nil, ClientError{Msg: "Only a draft vendor bill can be edited."}
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `
		UPDATE vendor_bill SET
			vendor_bill_reference_number = $1, vendor_bill_date = COALESCE($2, vendor_bill_date),
			vendor_bill_due_date = $3, vendor_bill_owner_id = COALESCE($4, vendor_bill_owner_id),
			vendor_bill_memo = $5, vendor_bill_internal_notes = $6, vendor_bill_custom_fields = $7,
			vendor_bill_updated_at = NOW(), vendor_bill_updated_by = $8, vendor_bill_record_version = vendor_bill_record_version + 1
		WHERE vendor_bill_id = $9`,
		in.ReferenceNumber, in.BillDate, in.DueDate, in.OwnerEmployeeID,
		in.Memo, in.InternalNotes, custom, nullableInt(actorEmployeeID), internalID)
	if err != nil {
		return nil, fmt.Errorf("update vendor bill: %w", err)
	}
	return Get(ctx, pool, id)
}

// SoftDelete marks a vendor bill deleted (paired deleted_at/deleted_by).
// Allowed only while the bill is in DRFT or VOID; anything else is rejected
// with a ClientError.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	_, statusCode, err := internalIDAndStatusByUUID(ctx, pool, id)
	if err != nil {
		return err
	}
	if statusCode != "DRFT" && statusCode != "VOID" {
		return ClientError{Msg: "Only a draft or voided vendor bill can be deleted."}
	}
	deletedBy := actorOrSystem(actorEmployeeID)
	tag, err := pool.Exec(ctx, `
		UPDATE vendor_bill SET vendor_bill_deleted_at = NOW(), vendor_bill_deleted_by = $1
		WHERE vendor_bill_uuid = $2 AND vendor_bill_deleted_at IS NULL`, deletedBy, id)
	if err != nil {
		return fmt.Errorf("delete vendor bill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
