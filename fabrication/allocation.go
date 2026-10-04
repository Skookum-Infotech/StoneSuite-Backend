package fabrication

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
)

// Ledger event codes (inventory_slab_ledger.event) — see spec §2.9.
// Defined in terms of the inventory package's constants so the two cannot
// drift apart; the local names are kept because this module reads better with
// them.
const (
	ledgerReceived  = inventory.EventReceived
	ledgerConsumed  = inventory.EventConsumed
	ledgerRecovered = inventory.EventRecovered
	ledgerScrapped  = inventory.EventScrapped
)

// AllocateSlab reserves a specific physical slab against a job, row-locking the
// slab so concurrent reservations can't oversell. Legal from MALC onward,
// including CUTG and later so a broken slab can be replaced on a live job
// (spec §4.9). No stock change — reserving is not deducting (§4.1).
func AllocateSlab(ctx context.Context, pool *pgxpool.Pool, jobUUID, slabUUID string, pieceUUID string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin allocate slab: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, jobUUID)
	if err != nil {
		return err
	}
	if err := requireLegacyAllocation(ctx, tx, jobUUID); err != nil {
		return err
	}
	if st.statusCode == StatusDraft || st.statusCode == StatusOrderReceived {
		return ClientError{Msg: "Allocate material once the job has reached material allocation."}
	}
	if IsTerminal(st.statusCode) {
		return ClientError{Msg: "Cannot allocate material to a completed or cancelled job."}
	}

	if err := inventory.RequireInspectedTx(ctx, tx, slabUUID); err != nil {
		if inventory.IsClientError(err) {
			return ClientError{Msg: err.Error()}
		}
		return fmt.Errorf("check slab inspection: %w", err)
	}

	// Lock the slab and check availability (§4.2, §4.3).
	var slabID, slabItemID int
	var slabStatus string
	err = tx.QueryRow(ctx, `
		SELECT inventory_slab_id, inventory_item_id, slab_status FROM inventory_slab
		WHERE inventory_slab_uuid = $1 AND slab_deleted_at IS NULL
		FOR UPDATE`, slabUUID).Scan(&slabID, &slabItemID, &slabStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "Slab not found."}
	}
	if err != nil {
		return fmt.Errorf("lock slab: %w", err)
	}
	if slabStatus != "available" {
		return ClientError{Msg: fmt.Sprintf("Slab is %s and cannot be allocated.", slabStatus)}
	}
	// The sales order says what stone the customer bought, so a slab of a
	// different material cannot be cut for it. An order with no slab-tracked
	// catalogue line (a free-text stone line, say) names nothing to match, so it
	// does not restrict which slab is used.
	var matches bool
	if err := tx.QueryRow(ctx, `
		SELECT NOT EXISTS (
			SELECT 1 FROM sales_order_item soi JOIN inventory_item ii ON ii.inventory_item_id = soi.inventory_item_id
			WHERE soi.sales_order_id = $1 AND soi.item_deleted_at IS NULL AND ii.inventory_item_tracking = 'serialized'
		) OR EXISTS (
			SELECT 1 FROM sales_order_item soi
			WHERE soi.sales_order_id = $1 AND soi.item_deleted_at IS NULL AND soi.inventory_item_id = $2
		)`, st.salesOrderID, slabItemID).Scan(&matches); err != nil {
		return fmt.Errorf("check slab material against order: %w", err)
	}
	if !matches {
		return ClientError{Msg: "That slab is a different material from the ones on this job's sales order."}
	}

	var pieceID any
	if pieceUUID != "" {
		var id int
		err := tx.QueryRow(ctx, `
			SELECT fi.fabrication_job_item_id FROM fabrication_job_item fi
			WHERE fi.fabrication_job_item_uuid = $1 AND fi.fabrication_job_id = $2 AND fi.item_deleted_at IS NULL`,
			pieceUUID, st.internalID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ClientError{Msg: "Piece not found on this job."}
		}
		if err != nil {
			return fmt.Errorf("resolve piece: %w", err)
		}
		pieceID = id
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO fabrication_job_slab (fabrication_job_id, fabrication_job_item_id, inventory_slab_id, allocation_status, reserved_by)
		VALUES ($1, $2, $3, 'reserved', $4)`, st.internalID, pieceID, slabID, nullableInt(actorEmployeeID)); err != nil {
		// The partial unique index is the double-sell backstop → surface as 400.
		if isUniqueViolation(err) {
			return ClientError{Msg: "That slab is already allocated to a job."}
		}
		return fmt.Errorf("insert allocation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = 'reserved', slab_updated_at = NOW() WHERE inventory_slab_id = $1`, slabID); err != nil {
		return fmt.Errorf("mark slab reserved: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit allocate slab: %w", err)
	}
	return nil
}

// DeallocateSlab releases a still-reserved slab from a job (before cutting). A
// consumed slab cannot be deallocated — its stone is gone (§4.4).
func DeallocateSlab(ctx context.Context, pool *pgxpool.Pool, jobUUID, slabUUID string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin deallocate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := lockJob(ctx, tx, jobUUID); err != nil {
		return err
	}
	if err := requireLegacyAllocation(ctx, tx, jobUUID); err != nil {
		return err
	}

	var allocID, slabID int
	var allocStatus string
	err = tx.QueryRow(ctx, `
		SELECT fjs.fabrication_job_slab_id, fjs.inventory_slab_id, fjs.allocation_status
		FROM fabrication_job_slab fjs
		JOIN fabrication_job fj ON fj.fabrication_job_id = fjs.fabrication_job_id
		JOIN inventory_slab s ON s.inventory_slab_id = fjs.inventory_slab_id
		WHERE fj.fabrication_job_uuid = $1 AND s.inventory_slab_uuid = $2
		  AND fjs.allocation_status IN ('reserved','consumed')
		FOR UPDATE OF fjs`, jobUUID, slabUUID).Scan(&allocID, &slabID, &allocStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "No live allocation of that slab on this job."}
	}
	if err != nil {
		return fmt.Errorf("load allocation: %w", err)
	}
	if allocStatus == "consumed" {
		return ClientError{Msg: "A cut slab cannot be released; record a disposition instead."}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE fabrication_job_slab SET allocation_status = 'released' WHERE fabrication_job_slab_id = $1`, allocID); err != nil {
		return fmt.Errorf("release allocation: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = 'available', slab_updated_at = NOW() WHERE inventory_slab_id = $1`, slabID); err != nil {
		return fmt.Errorf("free slab: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit deallocate: %w", err)
	}
	return nil
}

// Action-based jobs must retain layout evidence and versioned reservation changes.
func requireLegacyAllocation(ctx context.Context, tx pgx.Tx, jobUUID string) error {
	var version int
	if err := tx.QueryRow(ctx, `SELECT workflow_version FROM fabrication_job WHERE fabrication_job_uuid=$1`, jobUUID).Scan(&version); err != nil {
		return fmt.Errorf("check allocation workflow: %w", err)
	}
	if version != WorkflowLegacy {
		return ErrActionConflict
	}
	return nil
}
