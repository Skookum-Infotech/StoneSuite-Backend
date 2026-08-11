package vendorpayment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// vendorSnapshot loads a vendor's internal id and display name for the
// create-time snapshot (spec AD-14). The display name prefers the
// organization legal name, falling back to the person given/family names.
func vendorSnapshot(ctx context.Context, q workflow.Querier, vendorUUID string) (id int, name string, err error) {
	err = q.QueryRow(ctx, `
		SELECT vendor_id,
		       CASE WHEN vendor_type = 'Organization' THEN vendor_legal_name
		            ELSE TRIM(vendor_given_name || ' ' || vendor_family_name) END
		FROM vendor WHERE vendor_uuid = $1 AND vendor_deleted_at IS NULL`, vendorUUID).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ClientError{Msg: "Unknown vendor."}
	}
	if err != nil {
		return 0, "", fmt.Errorf("load vendor snapshot: %w", err)
	}
	return id, name, nil
}

// resolveMethod validates that methodID references a live, active payment
// method row.
func resolveMethod(ctx context.Context, pool *pgxpool.Pool, methodID int) error {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT TRUE FROM lkp_payment_method WHERE payment_method_id = $1 AND payment_method_deleted_at IS NULL AND payment_method_is_active`,
		methodID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "Unknown or inactive payment method."}
	}
	if err != nil {
		return fmt.Errorf("resolve payment method: %w", err)
	}
	return nil
}

// Create inserts a new vendor payment header inside one transaction: resolves
// + snapshots the vendor (fixed at creation, spec AD-14), validates the
// payment method and custom fields, inserts the row, assigns the VPAY
// document number post-insert, and writes a 'create' history row. New
// payments start at DRFT, with applied_total 0 and unapplied_amount equal to
// the full amount. Any inline in.Applications run AFTER the header
// transaction commits, each as its own Apply call/transaction — so a later
// application failing does not roll back the header. This is the same
// accepted trade-off payment.Create documents: the header always persists;
// callers must check the returned error and reconcile any partially-applied
// state (e.g. via the returned payment's Applications) rather than assume
// all-or-nothing.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateVendorPaymentInput, actorEmployeeID int) (*VendorPayment, error) {
	if in.VendorUUID == "" {
		return nil, ClientError{Msg: "vendorUuid is required."}
	}
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	vendorID, vendorName, err := vendorSnapshot(ctx, pool, in.VendorUUID)
	if err != nil {
		return nil, err
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

	typeID, err := typeIDByCode(ctx, pool, "VPAY")
	if err != nil {
		return nil, err
	}
	draftStatusID, err := statusIDByCode(ctx, pool, typeID, "DRFT")
	if err != nil {
		return nil, err
	}

	ownerEmp := in.OwnerEmployeeID
	if ownerEmp == nil && actorEmployeeID != 0 {
		ownerEmp = &actorEmployeeID
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create vendor payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO vendor_payment (
			record_type, vendor_payment_status, vendor_payment_vendor_id, vendor_payment_vendor_name,
			vendor_payment_method, vendor_payment_reference_number, vendor_payment_date,
			vendor_payment_scheduled_date, vendor_payment_currency,
			vendor_payment_memo, vendor_payment_internal_notes,
			vendor_payment_amount, vendor_payment_applied_total, vendor_payment_unapplied_amount, vendor_payment_approval_status,
			vendor_payment_owner_id, vendor_payment_custom_fields, vendor_payment_created_by, vendor_payment_updated_by
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,COALESCE($7, CURRENT_DATE),
			$8,$9,
			$10,$11,
			$12,0,$12,'none',
			$13,$14,$15,$15
		) RETURNING vendor_payment_id, vendor_payment_uuid`,
		typeID, draftStatusID, vendorID, vendorName,
		in.MethodID, in.ReferenceNumber, in.PaymentDate,
		in.ScheduledDate, in.CurrencyID,
		in.Memo, in.InternalNotes,
		in.Amount,
		ownerEmp, custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert vendor payment: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE vendor_payment SET vendor_payment_number = $1 WHERE vendor_payment_id = $2`, number, newID); err != nil {
		return nil, fmt.Errorf("set vendor payment number: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_payment_history (vendor_payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, draftStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor payment create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create vendor payment: %w", err)
	}

	for _, app := range in.Applications {
		if _, err := Apply(ctx, pool, newUUID, app.VendorBillUUID, app.Amount, actorEmployeeID); err != nil {
			return nil, fmt.Errorf("apply inline application to vendor bill %s: %w", app.VendorBillUUID, err)
		}
	}
	return Get(ctx, pool, newUUID)
}
