package vendorpayment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// internalIDByUUID resolves a live vendor payment's internal id and current
// status code from its external uuid.
func internalIDByUUID(ctx context.Context, pool *pgxpool.Pool, id string) (int, string, error) {
	var internalID int
	var statusCode string
	err := pool.QueryRow(ctx, `
		SELECT vp.vendor_payment_id, rs.record_status_code
		FROM vendor_payment vp
		JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
		WHERE vp.vendor_payment_uuid = $1 AND vp.vendor_payment_deleted_at IS NULL`, id).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("resolve vendor payment: %w", err)
	}
	return internalID, statusCode, nil
}

// Update edits non-monetary fields only (spec AD-12: amount is immutable
// post-creation). Allowed only while the payment is DRFT or PAPV.
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in UpdateVendorPaymentInput, actorEmployeeID int) (*VendorPayment, error) {
	internalID, statusCode, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if statusCode != "DRFT" && statusCode != "PAPV" {
		return nil, ClientError{Msg: "Only a draft or pending-approval vendor payment can be edited."}
	}
	if err := resolveMethod(ctx, pool, in.MethodID); err != nil {
		return nil, err
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `
		UPDATE vendor_payment SET
			vendor_payment_method = $1, vendor_payment_reference_number = $2, vendor_payment_date = COALESCE($3, vendor_payment_date),
			vendor_payment_scheduled_date = $4, vendor_payment_currency = $5, vendor_payment_owner_id = COALESCE($6, vendor_payment_owner_id),
			vendor_payment_memo = $7, vendor_payment_internal_notes = $8, vendor_payment_custom_fields = $9,
			vendor_payment_updated_at = NOW(), vendor_payment_updated_by = $10, vendor_payment_record_version = vendor_payment_record_version + 1
		WHERE vendor_payment_id = $11`,
		in.MethodID, in.ReferenceNumber, in.PaymentDate, in.ScheduledDate, in.CurrencyID, in.OwnerEmployeeID,
		in.Memo, in.InternalNotes, custom, nullableInt(actorEmployeeID), internalID)
	if err != nil {
		return nil, fmt.Errorf("update vendor payment: %w", err)
	}
	return Get(ctx, pool, id)
}

// SoftDelete marks a vendor payment deleted (paired deleted_at/deleted_by).
// Blocked (ClientError, maps to 409) while any live vendor_payment_application
// references it.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	internalID, _, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return err
	}
	var liveApplications int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vendor_payment_application WHERE vendor_payment_id = $1 AND application_deleted_at IS NULL`,
		internalID).Scan(&liveApplications); err != nil {
		return fmt.Errorf("count live applications: %w", err)
	}
	if liveApplications > 0 {
		return ClientError{Msg: "Cannot delete a vendor payment with live applications; unapply or void it first."}
	}
	deletedBy := actorOrSystem(actorEmployeeID)
	tag, err := pool.Exec(ctx, `
		UPDATE vendor_payment SET vendor_payment_deleted_at = NOW(), vendor_payment_deleted_by = $1
		WHERE vendor_payment_uuid = $2 AND vendor_payment_deleted_at IS NULL`, deletedBy, id)
	if err != nil {
		return fmt.Errorf("delete vendor payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
