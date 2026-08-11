package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a vendor bill to toStatusCode after validating the move
// against the static transition map (spec §7, AD-15). Voiding is rejected
// (ClientError) while any live vendor_payment_application row references
// this bill — unapply/reverse those first, mirroring payment AD-11 applied
// to bill-void instead of payment-delete.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, vb.vendor_bill_status, vb.record_type, rs.record_status_code
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		FOR UPDATE OF vb`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve vendor bill for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	if toStatusCode == "VOID" {
		var liveApplications int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM vendor_payment_application vpa
			JOIN vendor_payment vp ON vp.vendor_payment_id = vpa.vendor_payment_id
			WHERE vpa.vendor_bill_id = $1 AND vpa.application_deleted_at IS NULL AND vp.vendor_payment_deleted_at IS NULL`,
			internalID).Scan(&liveApplications); err != nil {
			return nil, fmt.Errorf("count live vendor payment applications: %w", err)
		}
		if liveApplications > 0 {
			return nil, ClientError{Msg: "Cannot void a vendor bill with live payment applications; unapply them first."}
		}
	}

	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill SET vendor_bill_status = $1, vendor_bill_updated_at = NOW(),
			vendor_bill_updated_by = $2, vendor_bill_record_version = vendor_bill_record_version + 1
		WHERE vendor_bill_id = $3`, toStatusID, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update vendor bill status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_history (vendor_bill_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor bill transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}
