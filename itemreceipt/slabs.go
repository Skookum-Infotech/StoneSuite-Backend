// itemreceipt/slabs.go — receiving a serialized (slab-tracked) item slab by slab.
//
// A purchase-order receipt is the only way new slabs enter stock. A line for a
// serialized item therefore carries one row per physical slab instead of a typed
// quantity: the rows are validated and their area computed when the receipt is
// saved (computeSlabs), persisted in item_receipt_line_unit, and turned into
// inventory_slab rows -- with their serials -- when the receipt posts
// (slabPost.receiveLine). Voiding a posted receipt takes them back out.
package itemreceipt

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
	"stonesuite-backend/workflow"
)

const (
	// Column widths of inventory_slab / item_receipt_line_unit.
	maxSlabTextLen     = 50
	maxSupplierCodeLen = 80
	maxSerialLen       = 50

	// A slab's serial is <PO number>-<running number>, the number zero-padded to
	// at least serialSuffixDigits. serialSuffixMaxDigits bounds how much of an
	// existing serial's tail is read back as that number.
	serialSuffixDigits    = 3
	serialSuffixMaxDigits = 6

	// maxSlabDimensionMM (100 m) is far beyond any real slab; it exists so an
	// absurd value is refused as a 400 instead of overflowing DECIMAL(10,2).
	maxSlabDimensionMM = 100000

	areaScaleFactor = 1000 // slab_area is DECIMAL(14,3)

	// areaMatchTolerance is how far a stored slab area may drift from the area
	// recomputed at post time before the receipt is refused.
	areaMatchTolerance = 0.001
)

// resolvedSlab is a validated slab ready to insert.
type resolvedSlab struct {
	lengthMM, widthMM, thicknessMM float64
	area                           float64
	binID                          *int
	blockID, lot, grade            string
	supplierCode                   string
}

// fromInventoryErr re-wraps the inventory package's client errors as this
// package's, so the controller keeps mapping them to 400 instead of 500.
func fromInventoryErr(err error) error {
	if err != nil && inventory.IsClientError(err) {
		return ClientError{Msg: err.Error()}
	}
	return err
}

func roundArea(v float64) float64 { return math.Round(v*areaScaleFactor) / areaScaleFactor }

// computeSlabs validates one serialized line's slabs and computes each one's
// area (and their total) in the item's own unit. Pure: no database. Bins are
// resolved separately because that needs one.
func computeSlabs(lineNo int, info inventory.ItemUnitInfo, in []SlabInput) ([]resolvedSlab, float64, error) {
	if info.UnitCategory != inventory.UnitCategoryArea {
		return nil, 0, ClientError{Msg: fmt.Sprintf(
			"Line %d: slab-tracked items must use an area unit; %s is a %s unit.", lineNo, info.UnitCode, info.UnitCategory)}
	}
	if len(in) == 0 {
		return nil, 0, ClientError{Msg: fmt.Sprintf(
			"Line %d: add at least one slab (length, width and thickness) — this item is received slab by slab.", lineNo)}
	}
	out := make([]resolvedSlab, 0, len(in))
	seenSupplierCode := map[string]bool{}
	var total float64
	for i, s := range in {
		where := fmt.Sprintf("Line %d, slab %d", lineNo, i+1)
		if !(s.LengthMM > 0 && s.WidthMM > 0 && s.ThicknessMM > 0) {
			return nil, 0, ClientError{Msg: where + ": length, width and thickness must all be greater than zero."}
		}
		if s.LengthMM > maxSlabDimensionMM || s.WidthMM > maxSlabDimensionMM || s.ThicknessMM > maxSlabDimensionMM {
			return nil, 0, ClientError{Msg: fmt.Sprintf("%s: a dimension cannot exceed %d mm.", where, maxSlabDimensionMM)}
		}
		block, lot, grade := strings.TrimSpace(s.BlockID), strings.TrimSpace(s.Lot), strings.TrimSpace(s.Grade)
		supplier := strings.TrimSpace(s.SupplierCode)
		for _, f := range []struct{ name, v string }{{"Block ID", block}, {"Lot", lot}, {"Grade", grade}} {
			if len(f.v) > maxSlabTextLen {
				return nil, 0, ClientError{Msg: fmt.Sprintf("%s: %s cannot exceed %d characters.", where, f.name, maxSlabTextLen)}
			}
		}
		if len(supplier) > maxSupplierCodeLen {
			return nil, 0, ClientError{Msg: fmt.Sprintf("%s: supplier code cannot exceed %d characters.", where, maxSupplierCodeLen)}
		}
		if supplier != "" {
			key := strings.ToLower(supplier)
			if seenSupplierCode[key] {
				return nil, 0, ClientError{Msg: fmt.Sprintf("%s: supplier code %q is used on two slabs.", where, supplier)}
			}
			seenSupplierCode[key] = true
		}
		area, err := inventory.AreaFor(s.LengthMM, s.WidthMM, info.UnitCode, info.UnitCategory)
		if err != nil {
			return nil, 0, ClientError{Msg: where + ": " + err.Error()}
		}
		if area <= 0 {
			return nil, 0, ClientError{Msg: where + ": the slab is too small to have a measurable area."}
		}
		total += area
		out = append(out, resolvedSlab{
			lengthMM: s.LengthMM, widthMM: s.WidthMM, thicknessMM: s.ThicknessMM, area: area,
			blockID: block, lot: lot, grade: grade, supplierCode: supplier,
		})
	}
	return out, roundArea(total), nil
}

// insertLineSlabs stores a line's slab rows. Serial and slab id stay empty until
// the receipt posts.
func insertLineSlabs(ctx context.Context, tx pgx.Tx, lineID, lineNo int, slabs []resolvedSlab) error {
	for i, s := range slabs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO item_receipt_line_unit (
				item_receipt_line_id, unit_seq,
				slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area,
				inventory_bin_id, slab_block_id, slab_lot, slab_grade, slab_supplier_code)
			VALUES ($1,$2, $3,$4,$5,$6, $7,$8,$9,$10,$11)`,
			lineID, i+1,
			s.lengthMM, s.widthMM, s.thicknessMM, s.area,
			s.binID, s.blockID, s.lot, s.grade, s.supplierCode,
		); err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d, slab %d: an invalid bin was referenced.", lineNo, i+1)}
			}
			return fmt.Errorf("insert item receipt slab: %w", err)
		}
	}
	return nil
}

// ---- serial numbering -------------------------------------------------------

// serialPrefix is the fixed part of every serial minted for a purchase order.
func serialPrefix(poNumber string) (string, error) {
	po := strings.TrimSpace(poNumber)
	if po == "" {
		return "", ClientError{Msg: "The purchase order has no number yet, so slab serials cannot be assigned."}
	}
	prefix := po + "-"
	if len(prefix)+serialSuffixDigits > maxSerialLen {
		return "", ClientError{Msg: "The purchase order number is too long to build slab serials from."}
	}
	return prefix, nil
}

// slabSerial formats the n-th serial under a prefix, e.g. PO-000012-003.
func slabSerial(prefix string, n int) string {
	return fmt.Sprintf("%s%0*d", prefix, serialSuffixDigits, n)
}

// nextSlabNumber returns the number the next slab under prefix should take: one
// past the highest suffix ever used, counting scrapped and soft-deleted units so
// a serial is never handed out twice (a voided receipt's slabs keep theirs).
func nextSlabNumber(ctx context.Context, q workflow.Querier, prefix string) (int, error) {
	var highest int
	err := q.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(MAX(substr(slab_serial, length($1::text) + 1)::int), 0)
		FROM inventory_slab
		WHERE starts_with(slab_serial, $1::text)
		  AND substr(slab_serial, length($1::text) + 1) ~ '^[0-9]{1,%d}$'`, serialSuffixMaxDigits),
		prefix).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("find highest slab serial: %w", err)
	}
	return highest + 1, nil
}

// NextSlabSequence previews how the next slabs received against a purchase
// order will be numbered, so the receiving form can show serials before
// posting. Advisory only: posting assigns the real ones under the order's lock.
func NextSlabSequence(ctx context.Context, q workflow.Querier, poUUID string) (*SlabSequence, error) {
	var poNumber string
	err := q.QueryRow(ctx, `
		SELECT COALESCE(purchase_order_number,'') FROM purchase_order
		WHERE purchase_order_uuid = $1 AND purchase_order_deleted_at IS NULL`, poUUID).Scan(&poNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown purchase order."}
	}
	if err != nil {
		return nil, fmt.Errorf("load purchase order number: %w", err)
	}
	prefix, err := serialPrefix(poNumber)
	if err != nil {
		return nil, err
	}
	next, err := nextSlabNumber(ctx, q, prefix)
	if err != nil {
		return nil, err
	}
	return &SlabSequence{Prefix: prefix, Next: next}, nil
}

// ---- reading ----------------------------------------------------------------

// loadSlabs returns a receipt's slabs grouped by the uuid of the line they
// belong to, in the order they were entered.
func loadSlabs(ctx context.Context, q workflow.Querier, irInternalID int) (map[string][]LineSlab, error) {
	rows, err := q.Query(ctx, `
		SELECT irl.item_receipt_line_uuid, iru.slab_serial,
		       iru.slab_length_mm, iru.slab_width_mm, iru.slab_thickness_mm, iru.slab_area,
		       b.inventory_bin_uuid, COALESCE(b.bin_path,''),
		       iru.slab_block_id, iru.slab_lot, iru.slab_grade, iru.slab_supplier_code,
		       s.inventory_slab_uuid, COALESCE(s.slab_status,'')
		FROM item_receipt_line_unit iru
		JOIN item_receipt_line irl ON irl.item_receipt_line_id = iru.item_receipt_line_id
		LEFT JOIN inventory_bin b  ON b.inventory_bin_id = iru.inventory_bin_id
		LEFT JOIN inventory_slab s ON s.inventory_slab_id = iru.inventory_slab_id
		WHERE irl.item_receipt_id = $1 AND irl.item_deleted_at IS NULL
		ORDER BY irl.line_number, iru.unit_seq`, irInternalID)
	if err != nil {
		return nil, fmt.Errorf("load item receipt slabs: %w", err)
	}
	defer rows.Close()
	out := map[string][]LineSlab{}
	for rows.Next() {
		var (
			lineUUID string
			s        LineSlab
		)
		if err := rows.Scan(&lineUUID, &s.Serial,
			&s.LengthMM, &s.WidthMM, &s.ThicknessMM, &s.Area,
			&s.BinID, &s.BinPath,
			&s.BlockID, &s.Lot, &s.Grade, &s.SupplierCode,
			&s.UnitID, &s.UnitStatus); err != nil {
			return nil, fmt.Errorf("scan item receipt slab: %w", err)
		}
		out[lineUUID] = append(out[lineUUID], s)
	}
	return out, rows.Err()
}

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
			Serial:       serial,
			VendorID:     &vendorID,
			SupplierCode: r.supplierCode,
			Item:         item,
			WarehouseID:  sp.warehouseID,
			BinID:        r.binID,
			BlockID:      r.blockID,
			Lot:          r.lot,
			LengthMM:     r.lengthMM,
			WidthMM:      r.widthMM,
			ThicknessMM:  r.thicknessMM,
			Grade:        r.grade,
			ReceivedAt:   sp.receiptDate,
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
