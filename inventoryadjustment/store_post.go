package inventoryadjustment

// store_post.go — the transition endpoint and the one move that touches stock.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docflow"
	"stonesuite-backend/inventory"
)

// Transition moves a document along its status graph.
//
// Posting is refused here and routed to Post, which does the stock work
// transactionally. Letting a bare status write reach POST would move the
// document into its terminal state without moving any stock — and because POST
// is terminal, there would be no way back to make it right.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode, note string, actorEmployeeID int) (*Adjustment, error) {
	if toStatusCode == StatusPosted {
		return nil, ClientError{Msg: "Use the post endpoint to apply an adjustment to stock."}
	}
	if !machine.Known(toStatusCode) {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, fromStatusID, fromCode, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if err := machine.Validate(fromCode, toStatusCode); err != nil {
		return nil, ClientError{Msg: fmt.Sprintf("An adjustment cannot go from %s to %s.", fromCode, toStatusCode)}
	}

	recordTypeID, err := docflow.RecordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, err
	}
	toStatusID, err := docflow.StatusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	set := `adjustment_status = $2, adjustment_updated_at = NOW(), adjustment_updated_by = $3,
	        adjustment_record_version = adjustment_record_version + 1`
	if toStatusCode == StatusCancelled {
		set += `, adjustment_cancelled_at = NOW(), adjustment_cancelled_by = $3,
		         adjustment_cancel_reason = $4`
	}
	args := []any{id, toStatusID, nullableInt(actorEmployeeID)}
	if toStatusCode == StatusCancelled {
		// chk_iadj_cancel_pair needs both halves, so an unresolved actor has to
		// fall back rather than write NULL.
		args[2] = actorOrSystem(actorEmployeeID)
		args = append(args, note)
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory_adjustment SET `+set+`
		WHERE inventory_adjustment_id = $1`, args...); err != nil {
		return nil, mapWriteErr(err, "transition")
	}

	action := "transition"
	if toStatusCode == StatusApproved {
		action = "approve"
	}
	if err := writeHistory(ctx, tx, id, action, &fromStatusID, &toStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// Post applies an approved adjustment to stock and closes it.
//
// Every line moves inside ONE transaction: a half-applied adjustment would
// leave the ledger disagreeing with the document that explains it, and there is
// no reversal path to clean that up.
func Post(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Adjustment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin post adjustment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, fromStatusID, fromCode, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if fromCode == StatusPosted {
		return nil, ClientError{Msg: "This adjustment has already been posted."}
	}
	if fromCode != StatusApproved {
		return nil, ClientError{Msg: "Only an approved adjustment can be posted."}
	}

	recordTypeID, err := docflow.RecordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, err
	}
	postedStatusID, err := docflow.StatusIDByCode(ctx, tx, recordTypeID, StatusPosted)
	if err != nil {
		return nil, err
	}

	warehouseID, err := warehouseOf(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	lines, err := postableLines(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return nil, ClientError{Msg: "This adjustment has no lines to post."}
	}

	for _, l := range lines {
		src := inventory.DocSource{RecordTypeID: recordTypeID, RecordID: id, LineID: l.id}
		if err := postLine(ctx, tx, l, warehouseID, src, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_adjustment SET
			adjustment_status = $2, adjustment_posted_at = NOW(), adjustment_posted_by = $3,
			adjustment_updated_at = NOW(), adjustment_updated_by = $3,
			adjustment_record_version = adjustment_record_version + 1
		WHERE inventory_adjustment_id = $1`,
		id, postedStatusID, actorOrSystem(actorEmployeeID)); err != nil {
		return nil, mapWriteErr(err, "post")
	}
	if err := writeHistory(ctx, tx, id, "post", &fromStatusID, &postedStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit post adjustment: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// postableLine is one line's posting shape.
type postableLine struct {
	id       int
	itemID   int
	slabUUID *string
	reasonID int
	qtyDelta float64
	notes    string
}

func warehouseOf(ctx context.Context, tx pgx.Tx, id int) (int, error) {
	var warehouseID int
	if err := tx.QueryRow(ctx, `
		SELECT warehouse_id FROM inventory_adjustment WHERE inventory_adjustment_id = $1`,
		id).Scan(&warehouseID); err != nil {
		return 0, fmt.Errorf("read adjustment warehouse: %w", err)
	}
	return warehouseID, nil
}

func postableLines(ctx context.Context, tx pgx.Tx, id int) ([]postableLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.inventory_adjustment_line_id, l.inventory_item_id, s.inventory_slab_uuid,
		       l.inventory_reason_id, l.qty_delta, l.line_notes
		FROM inventory_adjustment_line l
		LEFT JOIN inventory_slab s ON s.inventory_slab_id = l.inventory_slab_id
		WHERE l.inventory_adjustment_id = $1 AND l.line_deleted_at IS NULL
		ORDER BY l.line_number`, id)
	if err != nil {
		return nil, fmt.Errorf("load postable lines: %w", err)
	}
	defer rows.Close()
	var out []postableLine
	for rows.Next() {
		var l postableLine
		if err := rows.Scan(&l.id, &l.itemID, &l.slabUUID, &l.reasonID, &l.qtyDelta, &l.notes); err != nil {
			return nil, fmt.Errorf("scan postable line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load postable lines: %w", err)
	}
	return out, nil
}

// postLine moves one line's stock. A serialized line goes through inventory's
// slab operations so the slab's status, its ledger row and its history stay in
// step; a bulk line writes straight to the bulk ledger.
func postLine(ctx context.Context, tx pgx.Tx, l postableLine, warehouseID int,
	src inventory.DocSource, actorEmployeeID int) error {
	if l.slabUUID == nil {
		err := inventory.LedgerAndStockFromDoc(ctx, tx, l.itemID, warehouseID,
			inventory.EventAdjusted, l.qtyDelta, src, actorEmployeeID)
		if errors.Is(err, inventory.ErrMovementAlreadyApplied) {
			return ClientError{Msg: "This adjustment has already been applied to stock."}
		}
		return err
	}

	// Re-read the slab under a lock at post time. The line snapshotted its area
	// when it was drafted, and the slab may have been cut or scrapped since —
	// posting the stale number would deduct stone that is no longer there.
	unit, err := inventory.ResolveUnitForDocument(ctx, tx, *l.slabUUID, true)
	if err != nil {
		if err == inventory.ErrNotFound {
			return ClientError{Msg: "A unit on this adjustment no longer exists."}
		}
		return err
	}
	reason := l.reasonID
	if l.qtyDelta < 0 {
		_, err = inventory.AdjustSlabDown(ctx, tx, unit, &reason, l.notes, src, actorEmployeeID)
	} else {
		_, err = inventory.AdjustSlabUp(ctx, tx, unit, &reason, l.notes, src, actorEmployeeID)
	}
	if errors.Is(err, inventory.ErrMovementAlreadyApplied) {
		return ClientError{Msg: fmt.Sprintf("Unit %s has already been adjusted by this document.", unit.Serial)}
	}
	return err
}
