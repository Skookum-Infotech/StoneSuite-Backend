package inventorycount

// store_post.go — the transition endpoint and posting the variances.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/docflow"
	"stonesuite-backend/inventory"
)

// Transition moves the document along its status graph.
//
// Freezing and posting are refused here and routed to Freeze and Post, which do
// their work transactionally. A bare status write into CNTG would mark the count
// as started without taking the snapshot the whole document depends on.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode, note string, actorEmployeeID int) (*Count, error) {
	switch toStatusCode {
	case StatusCounting:
		// RVW_ -> CNTG is a recount against the SAME snapshot, so it is allowed
		// here; DRFT -> CNTG is the freeze and is not.
		if err := allowRecountOnly(ctx, pool, uuid); err != nil {
			return nil, err
		}
	case StatusPosted:
		return nil, ClientError{Msg: "Use the post endpoint to apply a count's variances to stock."}
	}
	if !machine.Known(toStatusCode) {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if err := machine.Validate(cur.statusCode, toStatusCode); err != nil {
		return nil, ClientError{Msg: fmt.Sprintf("A count cannot go from %s to %s.", cur.statusCode, toStatusCode)}
	}
	if toStatusCode == StatusInReview {
		if err := requireEveryLineCounted(ctx, tx, cur.id); err != nil {
			return nil, err
		}
	}

	recordTypeID, err := docflow.RecordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, err
	}
	toStatusID, err := docflow.StatusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	set := `count_status = $2, count_updated_at = NOW(), count_updated_by = $3,
	        count_record_version = count_record_version + 1`
	args := []any{cur.id, toStatusID, nullableInt(actorEmployeeID)}
	if toStatusCode == StatusCancelled {
		set += `, count_cancelled_at = NOW(), count_cancelled_by = $3, count_cancel_reason = $4`
		args[2] = actorOrSystem(actorEmployeeID)
		args = append(args, note)
	}
	if _, err := tx.Exec(ctx, `UPDATE inventory_count SET `+set+`
		WHERE inventory_count_id = $1`, args...); err != nil {
		return nil, mapWriteErr(err, "transition")
	}

	action := "transition"
	if toStatusCode == StatusApproved {
		action = "approve"
	}
	if err := writeHistory(ctx, tx, cur.id, action, &cur.statusID, &toStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// allowRecountOnly refuses DRFT -> CNTG through the plain transition endpoint,
// which would skip the snapshot.
func allowRecountOnly(ctx context.Context, pool *pgxpool.Pool, uuid string) error {
	c, err := Get(ctx, pool, uuid)
	if err != nil {
		return err
	}
	if c.StatusCode == StatusDraft {
		return ClientError{Msg: "Use the freeze endpoint to start counting."}
	}
	return nil
}

// requireEveryLineCounted refuses to send a count to review with lines the crew
// never reached.
//
// An uncounted line has a NULL counted_qty and so a NULL variance, which posting
// skips — meaning an unfinished count would post silently as "no discrepancy"
// for everything nobody looked at.
func requireEveryLineCounted(ctx context.Context, tx pgx.Tx, id int) error {
	var missing int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_count_line
		WHERE inventory_count_id = $1 AND line_deleted_at IS NULL AND counted_qty IS NULL`,
		id).Scan(&missing); err != nil {
		return fmt.Errorf("check uncounted lines: %w", err)
	}
	if missing > 0 {
		return ClientError{Msg: fmt.Sprintf(
			"%d line(s) have not been counted yet. Count them, or cancel the count.", missing)}
	}
	return nil
}

// Post applies an approved count's variances to stock.
//
// Variances post as 'adjusted' against the COUNT itself rather than by raising a
// separate adjustment document: the ledger row then points at the thing that
// actually explains it, and there is no second document to keep in step.
func Post(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Count, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin post count: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if cur.statusCode == StatusPosted {
		return nil, ClientError{Msg: "This count has already been posted."}
	}
	if cur.statusCode != StatusApproved {
		return nil, ClientError{Msg: "Only an approved count can be posted."}
	}

	recordTypeID, err := docflow.RecordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, err
	}
	postedStatusID, err := docflow.StatusIDByCode(ctx, tx, recordTypeID, StatusPosted)
	if err != nil {
		return nil, err
	}

	lines, err := varianceLines(ctx, tx, cur.id)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		if l.reasonID == nil {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d has a discrepancy and needs a reason code before it can post.", l.lineNumber)}
		}
		src := inventory.DocSource{RecordTypeID: recordTypeID, RecordID: cur.id, LineID: l.id}
		if err := postVariance(ctx, tx, l, cur.warehouseID, src, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_count SET count_status = $2,
			count_posted_at = NOW(), count_posted_by = $3,
			count_updated_at = NOW(), count_updated_by = $3,
			count_record_version = count_record_version + 1
		WHERE inventory_count_id = $1`,
		cur.id, postedStatusID, actorOrSystem(actorEmployeeID)); err != nil {
		return nil, mapWriteErr(err, "post")
	}
	if err := writeHistory(ctx, tx, cur.id, "post", &cur.statusID, &postedStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit post count: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// varianceLine is one discrepancy to post. Lines that agree with the system are
// not selected — they have nothing to move.
type varianceLine struct {
	id         int
	lineNumber int
	itemID     int
	slabUUID   *string
	reasonID   *int
	variance   float64
	notes      string
	serial     string
}

func varianceLines(ctx context.Context, tx pgx.Tx, id int) ([]varianceLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT l.inventory_count_line_id, l.line_number, l.inventory_item_id,
		       s.inventory_slab_uuid, l.inventory_reason_id, l.count_variance,
		       l.line_notes, l.slab_serial
		FROM inventory_count_line l
		LEFT JOIN inventory_slab s ON s.inventory_slab_id = l.inventory_slab_id
		WHERE l.inventory_count_id = $1 AND l.line_deleted_at IS NULL
		  AND l.count_variance IS NOT NULL AND l.count_variance <> 0
		ORDER BY l.line_number`, id)
	if err != nil {
		return nil, fmt.Errorf("load variance lines: %w", err)
	}
	defer rows.Close()
	var out []varianceLine
	for rows.Next() {
		var l varianceLine
		if err := rows.Scan(&l.id, &l.lineNumber, &l.itemID, &l.slabUUID,
			&l.reasonID, &l.variance, &l.notes, &l.serial); err != nil {
			return nil, fmt.Errorf("scan variance line: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// postVariance moves one line's discrepancy.
func postVariance(ctx context.Context, tx pgx.Tx, l varianceLine, warehouseID int,
	src inventory.DocSource, actorEmployeeID int) error {
	if l.slabUUID == nil {
		err := inventory.LedgerAndStockFromDoc(ctx, tx, l.itemID, warehouseID,
			inventory.EventAdjusted, l.variance, src, actorEmployeeID)
		if errors.Is(err, inventory.ErrMovementAlreadyApplied) {
			return ClientError{Msg: "This count has already been posted to stock."}
		}
		return err
	}
	unit, err := inventory.ResolveUnitForDocument(ctx, tx, *l.slabUUID, true)
	if err != nil {
		if err == inventory.ErrNotFound {
			return ClientError{Msg: fmt.Sprintf("Unit %s no longer exists.", l.serial)}
		}
		return err
	}
	if l.variance < 0 {
		// Counted absent: the stone is not on the rack, so it is written off.
		_, err = inventory.AdjustSlabDown(ctx, tx, unit, l.reasonID, l.notes, src, actorEmployeeID)
	} else {
		_, err = inventory.AdjustSlabUp(ctx, tx, unit, l.reasonID, l.notes, src, actorEmployeeID)
	}
	if errors.Is(err, inventory.ErrMovementAlreadyApplied) {
		return ClientError{Msg: fmt.Sprintf("Unit %s has already been posted by this count.", unit.Serial)}
	}
	return err
}
