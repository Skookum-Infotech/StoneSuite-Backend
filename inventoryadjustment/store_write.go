package inventoryadjustment

// store_write.go — create, update and delete.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docflow"
)

func validateHeader(in *Input) error {
	if in.WarehouseID <= 0 {
		return ClientError{Msg: "An adjustment needs a warehouse."}
	}
	return nil
}

// Create opens a new adjustment in draft with its lines attached.
func Create(ctx context.Context, pool *pgxpool.Pool, in Input, actorEmployeeID int) (*Adjustment, error) {
	if err := validateHeader(&in); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create adjustment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	recordTypeID, statusID, err := docflow.InitialStatus(ctx, tx, recordTypeCode, StatusDraft)
	if err != nil {
		return nil, err
	}
	lines, err := resolveLines(ctx, tx, in.WarehouseID, in.Lines)
	if err != nil {
		return nil, err
	}

	var (
		id      int
		newUUID string
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_adjustment (
			record_type, adjustment_status, warehouse_id, adjustment_date,
			inventory_reason_id, adjustment_notes, adjustment_internal_notes,
			adjustment_owner_id, adjustment_created_by
		) VALUES ($1,$2,$3, COALESCE($4::date, CURRENT_DATE), $5,$6,$7,$8,$9)
		RETURNING inventory_adjustment_id, inventory_adjustment_uuid`,
		recordTypeID, statusID, in.WarehouseID, orToday(in.Date),
		nullableIntPtr(in.ReasonID), in.Notes, in.InternalNotes,
		nullableIntPtr(in.OwnerID), nullableInt(actorEmployeeID),
	).Scan(&id, &newUUID)
	if err != nil {
		return nil, mapWriteErr(err, "insert")
	}

	// The human-readable number is derived from the serial PK, so it can only be
	// set after the insert. Same pattern as every other document here.
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_adjustment SET adjustment_number = $2 WHERE inventory_adjustment_id = $1`,
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
		return nil, fmt.Errorf("commit create adjustment: %w", err)
	}
	return Get(ctx, pool, newUUID)
}

// Update edits a draft adjustment, replacing its whole line set.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in Input, actorEmployeeID int) error {
	if err := validateHeader(&in); err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update adjustment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, statusID, statusCode, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if !IsEditable(statusCode) {
		// Editing after approval would mean the numbers that were signed off are
		// not the numbers that post.
		return ErrNotEditable
	}
	lines, err := resolveLines(ctx, tx, in.WarehouseID, in.Lines)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_adjustment SET
			warehouse_id = $2, adjustment_date = COALESCE($3::date, adjustment_date),
			inventory_reason_id = $4, adjustment_notes = $5, adjustment_internal_notes = $6,
			adjustment_owner_id = $7, adjustment_updated_at = NOW(), adjustment_updated_by = $8,
			adjustment_record_version = adjustment_record_version + 1
		WHERE inventory_adjustment_id = $1`,
		id, in.WarehouseID, orToday(in.Date), nullableIntPtr(in.ReasonID),
		in.Notes, in.InternalNotes, nullableIntPtr(in.OwnerID),
		nullableInt(actorEmployeeID)); err != nil {
		return mapWriteErr(err, "update")
	}
	if err := replaceLines(ctx, tx, id, lines, actorEmployeeID); err != nil {
		return err
	}
	if err := writeHistory(ctx, tx, id, "update", &statusID, &statusID, actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update adjustment: %w", err)
	}
	return nil
}

// Delete soft-deletes a draft adjustment.
//
// A posted adjustment is never deletable: its stock movement is in the ledger,
// and removing the document that explains it would leave an unattributed change
// in the audit trail. Cancel covers the "never mind" case before posting.
func Delete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete adjustment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, _, statusCode, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if statusCode == StatusPosted {
		return ClientError{Msg: "A posted adjustment cannot be deleted. Raise an opposite adjustment instead."}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_adjustment
		SET adjustment_deleted_at = NOW(), adjustment_deleted_by = $2
		WHERE inventory_adjustment_id = $1`, id, actorOrSystem(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete adjustment: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete adjustment: %w", err)
	}
	return nil
}
