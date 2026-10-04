package itemreceipt

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"math"
	"stonesuite-backend/inventory"
)

// ---- posting ----------------------------------------------------------------

// lineUnitRow is one stored slab row awaiting posting.
type lineUnitRow struct {
	id                             int
	lengthMM, widthMM, thicknessMM float64
	area                           float64
	binID                          *int
	blockID, lot, grade            string
	supplierCode                   string
}

// pendingLineUnits loads a line's slab rows that have not been turned into
// inventory yet, in entry order.
func pendingLineUnits(ctx context.Context, tx pgx.Tx, lineID int) ([]lineUnitRow, error) {
	rows, err := tx.Query(ctx, `
		SELECT item_receipt_line_unit_id, slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area,
		       inventory_bin_id, slab_block_id, slab_lot, slab_grade, slab_supplier_code
		FROM item_receipt_line_unit
		WHERE item_receipt_line_id = $1 AND inventory_slab_id IS NULL
		ORDER BY unit_seq`, lineID)
	if err != nil {
		return nil, fmt.Errorf("load receipt slabs for posting: %w", err)
	}
	defer rows.Close()
	var out []lineUnitRow
	for rows.Next() {
		var r lineUnitRow
		if err := rows.Scan(&r.id, &r.lengthMM, &r.widthMM, &r.thicknessMM, &r.area,
			&r.binID, &r.blockID, &r.lot, &r.grade, &r.supplierCode); err != nil {
			return nil, fmt.Errorf("scan receipt slab for posting: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// slabPost carries what turning a receipt's slab rows into inventory needs, and
// the running serial number shared by every serialized line on the receipt.
type slabPost struct {
	poNumber    string
	vendorID    int
	warehouseID int
	receiptDate string
	actor       int

	prefix string
	next   int // next serial number to hand out; 0 until first looked up
}

// receiveLine creates one inventory_slab (with ledger + history) for every slab
// row on a serialized line, assigns each its serial, and links row to slab. It
// runs inside the posting transaction under the purchase order's row lock, which
// is what serializes serial assignment for the order.
func (sp *slabPost) receiveLine(ctx context.Context, tx pgx.Tx, l postLine) error {
	rows, err := pendingLineUnits(ctx, tx, l.lineID)
	if err != nil {
		return err
	}
	if len(rows) == 0 || l.inventoryItemID == nil {
		return ClientError{Msg: fmt.Sprintf(
			"Line %d: slab details are required to post this receipt. Edit the receipt and add them.", l.lineNumber)}
	}
	item, err := inventory.ResolveItemUnitByID(ctx, tx, *l.inventoryItemID)
	if err != nil {
		return fromInventoryErr(err)
	}
	if sp.next == 0 {
		if sp.prefix, err = serialPrefix(sp.poNumber); err != nil {
			return err
		}
		if sp.next, err = nextSlabNumber(ctx, tx, sp.prefix); err != nil {
			return err
		}
	}

	for _, r := range rows {
		serial := slabSerial(sp.prefix, sp.next)
		sp.next++
		vendorID := sp.vendorID
		rec, err := inventory.ReceiveUnitTx(ctx, tx, inventory.ReceiveUnitParams{
			NeedsInspection: true,
			Serial:          serial,
			VendorID:        &vendorID,
			SupplierCode:    r.supplierCode,
			Item:            item,
			WarehouseID:     sp.warehouseID,
			BinID:           r.binID,
			BlockID:         r.blockID,
			Lot:             r.lot,
			LengthMM:        r.lengthMM,
			WidthMM:         r.widthMM,
			ThicknessMM:     r.thicknessMM,
			Grade:           r.grade,
			ReceivedAt:      sp.receiptDate,
		}, sp.actor)
		if err != nil {
			return fromInventoryErr(err)
		}
		// The line's quantity was fixed from this area when the receipt was
		// saved; if the item's unit has changed since, posting would leave the
		// order and the stock disagreeing.
		if math.Abs(rec.Area-r.area) > areaMatchTolerance {
			return ClientError{Msg: fmt.Sprintf(
				"Line %d: the item's unit of measure changed since this receipt was saved. Re-save the receipt and post again.", l.lineNumber)}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE item_receipt_line_unit SET slab_serial = $2, inventory_slab_id = $3
			WHERE item_receipt_line_unit_id = $1`, r.id, serial, rec.ID); err != nil {
			return fmt.Errorf("link receipt slab to inventory unit: %w", err)
		}
	}
	return nil
}

// ---- voiding ----------------------------------------------------------------

// postedSlabIDs returns the inventory units a line's posting created.
func postedSlabIDs(ctx context.Context, tx pgx.Tx, lineID int) ([]int, error) {
	rows, err := tx.Query(ctx, `
		SELECT inventory_slab_id FROM item_receipt_line_unit
		WHERE item_receipt_line_id = $1 AND inventory_slab_id IS NOT NULL
		ORDER BY unit_seq`, lineID)
	if err != nil {
		return nil, fmt.Errorf("load posted receipt slabs: %w", err)
	}
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan posted receipt slab: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
