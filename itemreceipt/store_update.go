// itemreceipt/store_update.go
package itemreceipt

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a pending receipt's header and lines.
//
// PEND-only (AD-5): once a receipt has posted, its quantities have moved
// through the ledger and into stock, and editing it in place would leave the
// ledger describing a document that no longer exists. Posted receipts are
// corrected by voiding and reissuing.
//
// Lines are replaced wholesale (soft-delete the old set, insert the new) rather
// than diffed: a pending receipt has no ledger rows pointing at its lines yet,
// so there is nothing for a stale line id to strand.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateItemReceiptInput, actorEmployeeID int) (*ItemReceipt, error) {
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update item receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var irInternalID, poInternalID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT ir.item_receipt_id, ir.purchase_order_id, rs.record_status_code
		FROM item_receipt ir
		JOIN lkp_record_status rs ON rs.record_status_id = ir.item_receipt_status
		WHERE ir.item_receipt_uuid = $1 AND ir.item_receipt_deleted_at IS NULL
		FOR UPDATE OF ir`, uuid,
	).Scan(&irInternalID, &poInternalID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load item receipt for update: %w", err)
	}
	if IsPosted(curStatusCode) {
		return nil, ErrAlreadyPosted
	}
	if curStatusCode != pendingStatusCode {
		return nil, ClientError{Msg: "Only a pending item receipt can be edited."}
	}

	lines, err := resolveLines(ctx, tx, poInternalID, in.Items)
	if err != nil {
		return nil, err
	}

	warehouseID := 0
	if in.WarehouseID != nil && *in.WarehouseID > 0 {
		warehouseID = *in.WarehouseID
	} else {
		warehouseID, err = defaultWarehouseID(ctx, tx)
		if err != nil {
			return nil, err
		}
	}

	ownerEmployeeID := 0
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE item_receipt SET
			warehouse_id = $2,
			item_receipt_date = $3::date,
			item_receipt_packing_slip = $4,
			item_receipt_carrier = $5,
			item_receipt_tracking_number = $6,
			item_receipt_bill_of_lading = $7,
			item_receipt_notes = $8,
			item_receipt_internal_notes = $9,
			item_receipt_owner_id = COALESCE($10, item_receipt_owner_id),
			item_receipt_custom_fields = $11,
			item_receipt_updated_at = NOW(),
			item_receipt_updated_by = $12,
			item_receipt_record_version = item_receipt_record_version + 1
		WHERE item_receipt_id = $1`,
		irInternalID, warehouseID, orNow(in.ReceiptDate),
		in.PackingSlip, in.Carrier, in.TrackingNumber, in.BillOfLading,
		in.Notes, in.InternalNotes,
		nullableInt(ownerEmployeeID), custom, nullableInt(actorEmployeeID),
	); err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (warehouse or owner) does not exist."}
		}
		return nil, fmt.Errorf("update item receipt: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE item_receipt_line SET item_deleted_at = NOW()
		WHERE item_receipt_id = $1 AND item_deleted_at IS NULL`, irInternalID); err != nil {
		return nil, fmt.Errorf("clear item receipt lines: %w", err)
	}
	if err := insertLines(ctx, tx, irInternalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, irInternalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update item receipt: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// SoftDelete marks a receipt deleted. Only PEND and VOID receipts may be
// deleted: a posted receipt is the audit trail for stock that actually moved,
// so it has to stay. Voiding first is the way to remove a posted receipt from
// the working set.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	if actorEmployeeID <= 0 {
		return ClientError{Msg: "A deleting employee is required."}
	}
	var curStatusCode string
	err := pool.QueryRow(ctx, `
		SELECT rs.record_status_code
		FROM item_receipt ir
		JOIN lkp_record_status rs ON rs.record_status_id = ir.item_receipt_status
		WHERE ir.item_receipt_uuid = $1 AND ir.item_receipt_deleted_at IS NULL`, uuid).Scan(&curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load item receipt for delete: %w", err)
	}
	if IsPosted(curStatusCode) {
		return ErrAlreadyPosted
	}

	tag, err := pool.Exec(ctx, `
		UPDATE item_receipt
		SET item_receipt_deleted_at = NOW(), item_receipt_deleted_by = $2
		WHERE item_receipt_uuid = $1 AND item_receipt_deleted_at IS NULL`, uuid, actorEmployeeID)
	if err != nil {
		return fmt.Errorf("delete item receipt: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
