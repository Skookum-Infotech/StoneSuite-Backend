package vendorbill

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

// Create inserts a new vendor bill header inside one transaction: resolves +
// snapshots the vendor (fixed at creation, spec AD-14), validates custom
// fields, inserts the row, assigns the VBIL document number post-insert,
// sets grand_total from the input (default 0) with balance_due mirroring it,
// and writes a 'create' history row. New bills start at DRFT.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateVendorBillInput, actorEmployeeID int) (*VendorBill, error) {
	if in.VendorUUID == "" {
		return nil, ClientError{Msg: "vendorUuid is required."}
	}
	if in.GrandTotal < 0 {
		return nil, ClientError{Msg: "grandTotal must not be negative."}
	}
	vendorID, vendorName, err := vendorSnapshot(ctx, pool, in.VendorUUID)
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

	typeID, err := typeIDByCode(ctx, pool, "VBIL")
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
		return nil, fmt.Errorf("begin create vendor bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO vendor_bill (
			record_type, vendor_bill_status, vendor_bill_vendor_id, vendor_bill_vendor_name,
			vendor_bill_reference_number, vendor_bill_date, vendor_bill_due_date,
			vendor_bill_memo, vendor_bill_internal_notes,
			vendor_bill_grand_total, vendor_bill_balance_due,
			vendor_bill_owner_id, vendor_bill_custom_fields, vendor_bill_created_by, vendor_bill_updated_by
		) VALUES (
			$1,$2,$3,$4,
			$5,COALESCE($6, CURRENT_DATE),$7,
			$8,$9,
			$10,$10,
			$11,$12,$13,$13
		) RETURNING vendor_bill_id, vendor_bill_uuid`,
		typeID, draftStatusID, vendorID, vendorName,
		in.ReferenceNumber, in.BillDate, in.DueDate,
		in.Memo, in.InternalNotes,
		in.GrandTotal,
		ownerEmp, custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert vendor bill: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE vendor_bill SET vendor_bill_number = $1 WHERE vendor_bill_id = $2`, number, newID); err != nil {
		return nil, fmt.Errorf("set vendor bill number: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_history (vendor_bill_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, draftStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor bill create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create vendor bill: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
