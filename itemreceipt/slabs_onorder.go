// itemreceipt/slabs_onorder.go — receiving the slabs a purchase order already put
// on order.
//
// When an order is sent, each of its sized slab lines gets one inventory_slab per
// ordered slab, status 'on_order' (package purchaseorder, inventory/on_order.go).
// The receiving screen lists them with a tick box; a receipt row that carries the
// uuid of such a slab (SlabInput.OnOrderSlabID) is that slab arriving. The slab
// keeps its serial and the size it was ordered at -- the receiver only supplies a
// thickness if the order left it out, and an optional bin -- and posting the
// receipt flips it to 'available'.
package itemreceipt

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/inventory"
	"stonesuite-backend/workflow"
)

// onOrderSlabRow is an On Order slab as the receipt sees it.
type onOrderSlabRow struct {
	id                             int
	uuid                           string
	serial                         string
	status                         string
	poItemID                       *int
	lengthMM, widthMM, thicknessMM float64
}

// splitOnOrderInputs separates a line's slab rows into the ones that tick an On
// Order slab and the ones the receiver typed in. Order within each group is kept.
func splitOnOrderInputs(in []SlabInput) (claimed, manual []SlabInput) {
	for _, s := range in {
		if strings.TrimSpace(s.OnOrderSlabID) != "" {
			claimed = append(claimed, s)
		} else {
			manual = append(manual, s)
		}
	}
	return claimed, manual
}

// loadOnOrderSlabs reads the named slabs by uuid. The uuids are compared as text
// so a malformed one is reported as an unknown slab instead of failing the query.
func loadOnOrderSlabs(ctx context.Context, q workflow.Querier, uuids []string) (map[string]onOrderSlabRow, error) {
	rows, err := q.Query(ctx, `
		SELECT inventory_slab_id, inventory_slab_uuid::text, slab_serial, slab_status,
		       purchase_order_item_id, slab_length_mm, slab_width_mm, slab_thickness_mm
		FROM inventory_slab
		WHERE inventory_slab_uuid::text = ANY($1::text[]) AND slab_deleted_at IS NULL`, uuids)
	if err != nil {
		return nil, fmt.Errorf("load on-order slabs: %w", err)
	}
	defer rows.Close()
	out := make(map[string]onOrderSlabRow, len(uuids))
	for rows.Next() {
		var r onOrderSlabRow
		if err := rows.Scan(&r.id, &r.uuid, &r.serial, &r.status, &r.poItemID,
			&r.lengthMM, &r.widthMM, &r.thicknessMM); err != nil {
			return nil, fmt.Errorf("scan on-order slab: %w", err)
		}
		out[strings.ToLower(r.uuid)] = r
	}
	return out, rows.Err()
}

// resolveOnOrderSlabs validates the On Order slabs a receipt line ticks and turns
// them into receipt rows, returning them with their total area in the item's unit.
//
// A ticked slab must be live, still On Order, and ordered on THIS purchase-order
// line, and may be ticked only once. Its size is the size it was ordered at --
// whatever the client sent is ignored; the thickness is the slab's own, or the
// request's when the order left it blank.
func resolveOnOrderSlabs(
	ctx context.Context, q workflow.Querier,
	lineNo, poItemInternalID int, info inventory.ItemUnitInfo, in []SlabInput,
) ([]resolvedSlab, float64, error) {
	uuids := make([]string, len(in))
	for i, s := range in {
		uuids[i] = strings.ToLower(strings.TrimSpace(s.OnOrderSlabID))
	}
	found, err := loadOnOrderSlabs(ctx, q, uuids)
	if err != nil {
		return nil, 0, err
	}

	out := make([]resolvedSlab, 0, len(in))
	seen := map[string]bool{}
	var total float64
	for i, s := range in {
		where := fmt.Sprintf("Line %d, slab %d", lineNo, i+1)
		row, ok := found[uuids[i]]
		if !ok {
			return nil, 0, ClientError{Msg: where + ": that slab was not found."}
		}
		if seen[row.uuid] {
			return nil, 0, ClientError{Msg: fmt.Sprintf("%s: slab %s is ticked twice.", where, row.serial)}
		}
		seen[row.uuid] = true
		if row.status != inventory.StatusOnOrder {
			return nil, 0, ClientError{Msg: fmt.Sprintf(
				"%s: slab %s is no longer on order (it is %s). Refresh the receipt and try again.", where, row.serial, row.status)}
		}
		if row.poItemID == nil || *row.poItemID != poItemInternalID {
			return nil, 0, ClientError{Msg: fmt.Sprintf("%s: slab %s was not ordered on this purchase order line.", where, row.serial)}
		}

		thickness := row.thicknessMM
		if !(thickness > 0) {
			thickness = s.ThicknessMM
		}
		if !(thickness > 0) {
			return nil, 0, ClientError{Msg: fmt.Sprintf("%s: slab %s needs a thickness greater than zero.", where, row.serial)}
		}
		if thickness > maxSlabDimensionMM {
			return nil, 0, ClientError{Msg: fmt.Sprintf("%s: a dimension cannot exceed %d mm.", where, maxSlabDimensionMM)}
		}
		area, err := inventory.AreaFor(row.lengthMM, row.widthMM, info.UnitCode, info.UnitCategory)
		if err != nil {
			return nil, 0, ClientError{Msg: where + ": " + err.Error()}
		}
		if area <= 0 {
			return nil, 0, ClientError{Msg: where + ": the slab is too small to have a measurable area."}
		}
		slabID := row.id
		total += area
		out = append(out, resolvedSlab{
			lengthMM: row.lengthMM, widthMM: row.widthMM, thicknessMM: thickness, area: area,
			expectedSlabID: &slabID,
		})
	}
	return out, roundArea(total), nil
}

// OnOrderSlabs lists the slabs of a purchase order that are still On Order, so
// the receiving screen can show them with a tick box each.
func OnOrderSlabs(ctx context.Context, q workflow.Querier, poUUID string) ([]inventory.OnOrderSlab, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM purchase_order
		               WHERE purchase_order_uuid = $1 AND purchase_order_deleted_at IS NULL)`, poUUID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check purchase order: %w", err)
	}
	if !exists {
		return nil, ClientError{Msg: "Unknown purchase order."}
	}
	return inventory.ListOnOrderForPO(ctx, q, poUUID)
}
