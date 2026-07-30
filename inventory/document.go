package inventory

// document.go — the primitives the Phase 3 document modules (adjustment,
// transfer, cycle count) post through.
//
// Those modules live in their own packages and must NOT reach into
// inventory_slab or the ledgers with raw SQL: the rules about what a slab's
// status may become, when its bin must be cleared and which ledger a movement
// belongs in are inventory's to keep. They call in here instead.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// UnitRef is everything a document line needs to know about one physical unit:
// enough to validate it, snapshot it onto the line, and post it.
type UnitRef struct {
	ID          int
	ItemID      int
	ItemUUID    string
	ItemName    string
	SKU         string
	WarehouseID int
	BinID       *int
	BinPath     string
	BundleID    *int
	Serial      string
	Status      string
	Area        float64
	UnitID      int
	UnitCode    string
}

// ResolveUnitForDocument loads a unit by uuid for a document line, locking it
// when forUpdate is set.
//
// Exported because the document modules address units by uuid, never by the
// SERIAL — the same rule every other API surface here follows.
func ResolveUnitForDocument(ctx context.Context, q pgxQuerier, uuid string, forUpdate bool) (UnitRef, error) {
	sql := `
		SELECT s.inventory_slab_id, s.inventory_item_id, ii.inventory_item_uuid,
		       ii.inventory_item_name, ii.inventory_item_sku,
		       s.warehouse_id, s.inventory_bin_id, COALESCE(b.bin_path,''),
		       s.inventory_bundle_id, s.slab_serial, s.slab_status, s.slab_area,
		       u.unit_id, u.unit_code
		FROM inventory_slab s
		JOIN inventory_item ii    ON ii.inventory_item_id = s.inventory_item_id
		JOIN lkp_unit u           ON u.unit_id = ii.inventory_item_unit_id
		LEFT JOIN inventory_bin b ON b.inventory_bin_id = s.inventory_bin_id
		WHERE s.inventory_slab_uuid = $1 AND s.slab_deleted_at IS NULL`
	if forUpdate {
		// OF s: locking the joined catalogue and lookup rows too would serialise
		// every document that touches the same item, for no added safety.
		sql += " FOR UPDATE OF s"
	}
	var r UnitRef
	err := q.QueryRow(ctx, sql, uuid).Scan(
		&r.ID, &r.ItemID, &r.ItemUUID, &r.ItemName, &r.SKU,
		&r.WarehouseID, &r.BinID, &r.BinPath, &r.BundleID,
		&r.Serial, &r.Status, &r.Area, &r.UnitID, &r.UnitCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return UnitRef{}, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return UnitRef{}, ErrNotFound
		}
		return UnitRef{}, fmt.Errorf("resolve unit for document: %w", err)
	}
	return r, nil
}

// ItemRef is a quantity-tracked item resolved for a document line.
type ItemRef struct {
	ID       int
	Name     string
	SKU      string
	UnitID   int
	UnitCode string
	Tracking string
}

// ResolveItemForDocument loads a catalogue item by uuid for a document line.
func ResolveItemForDocument(ctx context.Context, q pgxQuerier, uuid string) (ItemRef, error) {
	var r ItemRef
	err := q.QueryRow(ctx, `
		SELECT ii.inventory_item_id, ii.inventory_item_name, ii.inventory_item_sku,
		       u.unit_id, u.unit_code, ii.inventory_item_tracking
		FROM inventory_item ii
		JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_uuid = $1 AND ii.inventory_item_deleted_at IS NULL`,
		uuid).Scan(&r.ID, &r.Name, &r.SKU, &r.UnitID, &r.UnitCode, &r.Tracking)
	if errors.Is(err, pgx.ErrNoRows) {
		return ItemRef{}, ClientError{Msg: "The referenced inventory item does not exist."}
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ItemRef{}, ClientError{Msg: "The referenced inventory item does not exist."}
		}
		return ItemRef{}, fmt.Errorf("resolve item for document: %w", err)
	}
	return r, nil
}

// DocSource identifies the document line a movement is posted from. Every field
// is required: source_record_type is what keeps IADJ line 512 and ICNT line 512
// from colliding on the once-only indexes (AD-11).
type DocSource struct {
	RecordTypeID int
	RecordID     int
	LineID       int
}

// SlabLedgerAndStockFromDoc appends one inventory_slab_ledger row for a
// DOCUMENT-sourced serialized movement and applies the delta to inventory_stock.
//
// It differs from SlabLedgerAndStock in exactly one way, and the difference is
// deliberate — the same distinction that makes the bulk and serialized writers
// non-interchangeable (see the note at the top of ledger.go):
//
//   - SlabLedgerAndStock treats a duplicate as a NO-OP, because its callers
//     re-run idempotent state transitions (consuming an already-consumed slab).
//   - This one treats a duplicate as ErrMovementAlreadyApplied, because its
//     callers post documents. A document posting twice is a real error the user
//     must see, not something to swallow.
//
// Collapsing the two would silently double-post or silently drop a leg,
// depending on which behaviour won.
func SlabLedgerAndStockFromDoc(
	ctx context.Context, tx pgx.Tx,
	slabID, itemID, warehouseID int, event string, delta float64,
	src DocSource, actorEmployeeID int,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_slab_ledger (
			inventory_slab_id, inventory_item_id, warehouse_id, event, quantity_delta,
			source_record_type, source_record_id, source_line_id, actor_employee_id
		) VALUES ($1,$2,$3,$4,$5, $6,$7,$8,$9)`,
		slabID, itemID, warehouseID, event, delta,
		nullableInt(src.RecordTypeID), nullableInt(src.RecordID), nullableInt(src.LineID),
		nullableInt(actorEmployeeID),
	); err != nil {
		if isUniqueViolation(err) {
			return ErrMovementAlreadyApplied
		}
		return fmt.Errorf("insert slab ledger row from document: %w", err)
	}

	switch err := applyStockDelta(ctx, tx, itemID, warehouseID, delta); {
	case errors.Is(err, errStockWouldGoNegative):
		return ClientError{Msg: "This would drive stock below zero. Reconcile the item's on-hand quantity before posting."}
	case errors.Is(err, errNoStockRow):
		return ClientError{Msg: "No stock on hand for this item at this warehouse."}
	case err != nil:
		return err
	}
	return nil
}

// LedgerAndStockFromDoc is the bulk sibling, wrapping LedgerAndStock so the
// document modules use one source shape for both stock models.
func LedgerAndStockFromDoc(
	ctx context.Context, tx pgx.Tx,
	itemID, warehouseID int, event string, delta float64,
	src DocSource, actorEmployeeID int,
) error {
	return LedgerAndStock(ctx, tx, itemID, warehouseID, event, delta,
		src.RecordTypeID, src.RecordID, src.LineID, actorEmployeeID)
}
