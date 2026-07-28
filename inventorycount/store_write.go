package inventorycount

// store_write.go — create, update and delete.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docflow"
)

func validateHeader(in *Input) error {
	if in.WarehouseID <= 0 {
		return ClientError{Msg: "A count needs a warehouse."}
	}
	return nil
}

// resolveBin validates the optional scope bin belongs to the counted warehouse.
func resolveBin(ctx context.Context, q pgxQuerier, binUUID *string, warehouseID int) (*int, error) {
	if binUUID == nil || strings.TrimSpace(*binUUID) == "" {
		return nil, nil
	}
	var id, whID int
	if err := q.QueryRow(ctx, `
		SELECT inventory_bin_id, warehouse_id FROM inventory_bin
		WHERE inventory_bin_uuid = $1 AND bin_deleted_at IS NULL`, *binUUID).Scan(&id, &whID); err != nil {
		return nil, ClientError{Msg: "Unknown bin."}
	}
	if whID != warehouseID {
		return nil, ClientError{Msg: "That bin is not in the counted warehouse."}
	}
	return &id, nil
}

// Create opens a new count in draft. Its lines are built by Freeze, from what
// the system actually holds — never supplied by the caller.
func Create(ctx context.Context, pool *pgxpool.Pool, in Input, actorEmployeeID int) (*Count, error) {
	if err := validateHeader(&in); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create count: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recordTypeID, statusID, err := docflow.InitialStatus(ctx, tx, recordTypeCode, StatusDraft)
	if err != nil {
		return nil, err
	}
	binID, err := resolveBin(ctx, tx, in.BinID, in.WarehouseID)
	if err != nil {
		return nil, err
	}

	var (
		id      int
		newUUID string
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_count (
			record_type, count_status, warehouse_id, inventory_bin_id,
			count_date, count_notes, count_internal_notes, count_owner_id, count_created_by
		) VALUES ($1,$2,$3,$4, COALESCE($5::date, CURRENT_DATE), $6,$7,$8,$9)
		RETURNING inventory_count_id, inventory_count_uuid`,
		recordTypeID, statusID, in.WarehouseID, binID,
		nullableStr(in.Date), in.Notes, in.InternalNotes,
		nullableIntPtr(in.OwnerID), nullableInt(actorEmployeeID),
	).Scan(&id, &newUUID)
	if err != nil {
		return nil, mapWriteErr(err, "insert")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_count SET count_number = $2 WHERE inventory_count_id = $1`,
		id, FormatNumber(int64(id))); err != nil {
		return nil, mapWriteErr(err, "number")
	}
	if err := writeHistory(ctx, tx, id, "create", nil, &statusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create count: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

// Update edits a draft count's scope and paperwork.
//
// Only while it is a draft: once frozen, changing the scope would add lines
// whose snapshot was taken at a different moment from the rest, so the two
// halves of the count would be measured against different moments in time.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in Input, actorEmployeeID int) error {
	if err := validateHeader(&in); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update count: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if !IsEditable(cur.statusCode) {
		return ErrNotEditable
	}
	binID, err := resolveBin(ctx, tx, in.BinID, in.WarehouseID)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_count SET
			warehouse_id = $2, inventory_bin_id = $3,
			count_date = COALESCE($4::date, count_date),
			count_notes = $5, count_internal_notes = $6, count_owner_id = $7,
			count_updated_at = NOW(), count_updated_by = $8,
			count_record_version = count_record_version + 1
		WHERE inventory_count_id = $1`,
		cur.id, in.WarehouseID, binID, nullableStr(in.Date),
		in.Notes, in.InternalNotes, nullableIntPtr(in.OwnerID),
		nullableInt(actorEmployeeID)); err != nil {
		return mapWriteErr(err, "update")
	}
	if err := writeHistory(ctx, tx, cur.id, "update", &cur.statusID, &cur.statusID, actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update count: %w", err)
	}
	return nil
}

// Delete soft-deletes a count that has not posted.
//
// A posted count's variances are in the ledger, and removing the document that
// explains them would leave unattributed stock movements behind.
func Delete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete count: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if cur.statusCode == StatusPosted {
		return ClientError{Msg: "A posted count cannot be deleted."}
	}
	if IsFrozen(cur.statusCode) {
		// Deleting it would silently release the freeze on the counted scope
		// while the crew is still working in it.
		return ClientError{Msg: "This count is in progress. Cancel it before deleting."}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_count SET count_deleted_at = NOW(), count_deleted_by = $2
		WHERE inventory_count_id = $1`, cur.id, actorOrSystem(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete count: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete count: %w", err)
	}
	return nil
}
