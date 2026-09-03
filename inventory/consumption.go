package inventory

// consumption.go — raw per-item slab-ledger aggregates for the Material
// consumption dashboard widget.
//
// Slab movement is the only "material consumption" this schema can report:
// inventory_slab_ledger (see ledger.go) is written by exactly three paths —
// fabrication.ConsumeSlab (a job starts cutting a reserved slab), CutUnit
// (manually cutting a slab into remnants), and scrapping a unit — and nothing
// anywhere writes a bulk 'consumed' row to inventory_ledger. Quantity-tracked
// (non-slab) items have no comparable per-unit movement history, so this
// widget is scoped to slab-tracked items only, same as the fabrication and
// cutting flows it reflects.

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ConsumptionRow is one item's raw slab-ledger aggregate over a window. Signs
// follow the ledger's own convention (quantity_delta): ConsumedArea and
// ScrappedArea are stored as positive magnitudes here (the controller does
// the net/rank math), RecoveredArea is what came back as usable remnants.
// SlabCount is the number of distinct slabs that had a 'consumed' event in
// the window, for the row's "N slabs" sub-label.
type ConsumptionRow struct {
	ItemID        string
	ItemName      string
	UnitCode      string
	ColorHex      string
	ConsumedArea  float64
	RecoveredArea float64
	ScrappedArea  float64
	SlabCount     int
}

// MaterialConsumptionRows loads every slab-tracked item with any ledger
// activity since the given time (zero time.Time means unbounded — the
// dashboard's "all" range). No ORDER BY and no LIMIT: the controller ranks
// and truncates, matching the fetch/classify split used by
// StockAlertCandidates/classifyStockAlert.
func MaterialConsumptionRows(ctx context.Context, pool *pgxpool.Pool, since time.Time) ([]ConsumptionRow, error) {
	q := `
		SELECT ii.inventory_item_uuid, ii.inventory_item_name, u.unit_code,
		       COALESCE(c.color_hex, ''),
		       COALESCE(SUM(sl.quantity_delta) FILTER (WHERE sl.event = 'consumed'), 0),
		       COALESCE(SUM(sl.quantity_delta) FILTER (WHERE sl.event = 'recovered'), 0),
		       COALESCE(SUM(sl.quantity_delta) FILTER (WHERE sl.event = 'scrapped'), 0),
		       COUNT(DISTINCT sl.inventory_slab_id) FILTER (WHERE sl.event = 'consumed')
		FROM inventory_slab_ledger sl
		JOIN inventory_item ii ON ii.inventory_item_id = sl.inventory_item_id
		JOIN lkp_unit u        ON u.unit_id = ii.inventory_item_unit_id
		LEFT JOIN lkp_color c  ON c.color_id = ii.inventory_item_color_id
		WHERE ii.inventory_item_deleted_at IS NULL
		  AND ($1::timestamp IS NULL OR sl.occurred_at >= $1)
		GROUP BY ii.inventory_item_uuid, ii.inventory_item_name, u.unit_code, c.color_hex`

	var sinceParam any
	if !since.IsZero() {
		sinceParam = since
	}

	rows, err := pool.Query(ctx, q, sinceParam)
	if err != nil {
		return nil, fmt.Errorf("query material consumption rows: %w", err)
	}
	defer rows.Close()

	out := []ConsumptionRow{}
	for rows.Next() {
		var (
			row       ConsumptionRow
			consumed  float64
			recovered float64
			scrapped  float64
		)
		if err := rows.Scan(&row.ItemID, &row.ItemName, &row.UnitCode, &row.ColorHex,
			&consumed, &recovered, &scrapped, &row.SlabCount); err != nil {
			return nil, fmt.Errorf("scan material consumption row: %w", err)
		}
		// The ledger stores 'consumed'/'scrapped' as negative deltas and
		// 'recovered' as positive (see ledger.go) — normalize all three to
		// positive magnitudes here so the controller's net math
		// (consumed - recovered) reads naturally.
		row.ConsumedArea = -consumed
		row.RecoveredArea = recovered
		row.ScrappedArea = -scrapped
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query material consumption rows: %w", err)
	}
	return out, nil
}
