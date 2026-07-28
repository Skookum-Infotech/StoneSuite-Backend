package inventorytransfer

// store_write.go — create, update and delete.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docflow"
)

func validateHeader(in *Input) error {
	if in.FromWarehouseID <= 0 || in.ToWarehouseID <= 0 {
		return ClientError{Msg: "A transfer needs a source and a destination warehouse."}
	}
	if in.FromWarehouseID == in.ToWarehouseID {
		// Mirrors chk_itrf_distinct_wh, so the caller gets a sentence rather than
		// a constraint name.
		return ClientError{Msg: "A transfer's source and destination must be different warehouses."}
	}
	return nil
}

// resolveToBin validates the optional destination bin belongs to the
// destination warehouse.
//
// Bins are per-warehouse, so a bin from anywhere else would put arriving stone
// in a rack that physically stands in another building.
func resolveToBin(ctx context.Context, q pgxQuerier, binUUID *string, toWarehouseID int) (*int, error) {
	if binUUID == nil || strings.TrimSpace(*binUUID) == "" {
		return nil, nil
	}
	var id, whID int
	err := q.QueryRow(ctx, `
		SELECT inventory_bin_id, warehouse_id FROM inventory_bin
		WHERE inventory_bin_uuid = $1 AND bin_deleted_at IS NULL`, *binUUID).Scan(&id, &whID)
	if err != nil {
		return nil, ClientError{Msg: "Unknown destination bin."}
	}
	if whID != toWarehouseID {
		return nil, ClientError{Msg: "The destination bin is not in the destination warehouse."}
	}
	return &id, nil
}

// Create opens a new transfer in draft.
func Create(ctx context.Context, pool *pgxpool.Pool, in Input, actorEmployeeID int) (*Transfer, error) {
	if err := validateHeader(&in); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recordTypeID, statusID, err := docflow.InitialStatus(ctx, tx, recordTypeCode, StatusDraft)
	if err != nil {
		return nil, err
	}
	toBinID, err := resolveToBin(ctx, tx, in.ToBinID, in.ToWarehouseID)
	if err != nil {
		return nil, err
	}
	lines, err := resolveLines(ctx, tx, in.FromWarehouseID, in.Lines)
	if err != nil {
		return nil, err
	}

	var (
		id      int
		newUUID string
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_transfer (
			record_type, transfer_status, from_warehouse_id, to_warehouse_id, to_bin_id,
			transfer_date, transfer_expected_date, transfer_carrier, transfer_tracking_number,
			transfer_notes, transfer_internal_notes, transfer_owner_id, transfer_created_by
		) VALUES ($1,$2,$3,$4,$5, COALESCE($6::date, CURRENT_DATE), $7::date, $8,$9,$10,$11,$12,$13)
		RETURNING inventory_transfer_id, inventory_transfer_uuid`,
		recordTypeID, statusID, in.FromWarehouseID, in.ToWarehouseID, toBinID,
		nullableStr(&in.Date), nullableStr(in.ExpectedDate), in.Carrier, in.TrackingNumber,
		in.Notes, in.InternalNotes, nullableIntPtr(in.OwnerID), nullableInt(actorEmployeeID),
	).Scan(&id, &newUUID)
	if err != nil {
		return nil, mapWriteErr(err, "insert")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_transfer SET transfer_number = $2 WHERE inventory_transfer_id = $1`,
		id, FormatNumber(int64(id))); err != nil {
		return nil, mapWriteErr(err, "number")
	}
	if err := replaceLines(ctx, tx, id, lines, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := writeHistory(ctx, tx, id, "create", nil, &statusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create transfer: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

// Update edits a draft transfer, replacing its whole line set.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in Input, actorEmployeeID int) error {
	if err := validateHeader(&in); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if !IsEditable(cur.statusCode) {
		return ErrNotEditable
	}
	toBinID, err := resolveToBin(ctx, tx, in.ToBinID, in.ToWarehouseID)
	if err != nil {
		return err
	}
	lines, err := resolveLines(ctx, tx, in.FromWarehouseID, in.Lines)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_transfer SET
			from_warehouse_id = $2, to_warehouse_id = $3, to_bin_id = $4,
			transfer_date = COALESCE($5::date, transfer_date), transfer_expected_date = $6::date,
			transfer_carrier = $7, transfer_tracking_number = $8,
			transfer_notes = $9, transfer_internal_notes = $10, transfer_owner_id = $11,
			transfer_updated_at = NOW(), transfer_updated_by = $12,
			transfer_record_version = transfer_record_version + 1
		WHERE inventory_transfer_id = $1`,
		cur.id, in.FromWarehouseID, in.ToWarehouseID, toBinID,
		nullableStr(&in.Date), nullableStr(in.ExpectedDate), in.Carrier, in.TrackingNumber,
		in.Notes, in.InternalNotes, nullableIntPtr(in.OwnerID), nullableInt(actorEmployeeID)); err != nil {
		return mapWriteErr(err, "update")
	}
	if err := replaceLines(ctx, tx, cur.id, lines, actorEmployeeID); err != nil {
		return err
	}
	if err := writeHistory(ctx, tx, cur.id, "update", &cur.statusID, &cur.statusID, actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update transfer: %w", err)
	}
	return nil
}

// Delete soft-deletes a transfer that has not shipped.
//
// Once stock has left the source warehouse the document is the only record of
// where it went, so deleting it would strand that stock in in_transit with
// nothing to explain it.
func Delete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if HasShipped(cur.statusCode) {
		return ClientError{Msg: "A transfer that has shipped cannot be deleted."}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_transfer SET transfer_deleted_at = NOW(), transfer_deleted_by = $2
		WHERE inventory_transfer_id = $1`, cur.id, actorOrSystem(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete transfer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete transfer: %w", err)
	}
	return nil
}
