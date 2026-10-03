package inventory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InspectionInput records acceptance or rejection of an entire received slab.
type InspectionInput struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

// InspectUnit finalizes inspection once. Rejected slabs cannot be accepted by editing their location.
func InspectUnit(ctx context.Context, pool *pgxpool.Pool, unitUUID string, in InspectionInput, actor int) error {
	if in.Decision != "accepted" && in.Decision != "rejected" {
		return ClientError{Msg: "Choose accept or reject for the entire slab."}
	}
	if in.Decision == "rejected" && strings.TrimSpace(in.Reason) == "" {
		return ClientError{Msg: "A rejection reason is required."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin slab inspection: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var itemID, unitID int
	err = tx.QueryRow(ctx, `SELECT inventory_item_id,inventory_slab_id FROM inventory_slab WHERE inventory_slab_uuid=$1 AND slab_deleted_at IS NULL`, unitUUID).Scan(&itemID, &unitID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("resolve inspected slab: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT inventory_item_id FROM inventory_item WHERE inventory_item_id=$1 FOR UPDATE`, itemID); err != nil {
		return fmt.Errorf("lock inspected material: %w", err)
	}
	var status, inspection, reason string
	err = tx.QueryRow(ctx, `SELECT slab_status,inspection_status,inspection_reason FROM inventory_slab WHERE inventory_slab_uuid=$1 AND slab_deleted_at IS NULL FOR UPDATE`, unitUUID).Scan(&status, &inspection, &reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock inspected slab: %w", err)
	}
	if inspection == in.Decision && reason == in.Reason {
		return nil
	}
	if status != "available" || (inspection != "pending" && inspection != "legacy") {
		return ClientError{Msg: "Only an unallocated slab awaiting inspection can be inspected. Rejected slabs require supplier resolution."}
	}
	_, err = tx.Exec(ctx, `UPDATE inventory_slab SET inspection_status=$2,inspection_reason=$3,inspected_at=NOW(),inspected_by=$4 WHERE inventory_slab_uuid=$1`, unitUUID, in.Decision, in.Reason, nullableInt(actor))
	if err != nil {
		return fmt.Errorf("record slab inspection: %w", err)
	}
	if err = writeUnitHistory(ctx, tx, unitID, "status_change", "inspection_status", inspection, in.Decision, nil, nil, nil, in.Reason, actor); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit slab inspection: %w", err)
	}
	return nil
}

// RequireInspectedTx refuses pending or rejected stock at the physical consumption boundary.
func RequireInspectedTx(ctx context.Context, tx pgx.Tx, unitUUID string) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT inspection_status FROM inventory_slab WHERE inventory_slab_uuid=$1 AND slab_deleted_at IS NULL FOR UPDATE`, unitUUID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("check slab inspection: %w", err)
	}
	if status != "accepted" && status != "legacy" {
		return ClientError{Msg: "This slab has not passed inspection and cannot be allocated or cut."}
	}
	return nil
}
