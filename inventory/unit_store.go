package inventory

// unit_store.go — shared unit helpers and the single-row read.
// Moved here from fabrication/slab_store.go: a physical piece of stock is an
// inventory concern, and leaving its CRUD inside the fabrication module meant
// the yard could only be managed through a fabrication job.

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// unitFilterFrom is the FROM clause every unit query shares: the unit plus the
// tables its filter, sort and search expressions (unitResolver) refer to. It is
// its own constant so SummarizeUnits can filter through exactly the same joins
// as SearchUnits without dragging in the display-only ones below.
const unitFilterFrom = `
	FROM inventory_slab s
	JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
	JOIN company_location w ON w.company_location_id = s.warehouse_id
	JOIN lkp_unit au       ON au.unit_id = s.slab_area_unit_id
	LEFT JOIN inventory_bin b     ON b.inventory_bin_id = s.inventory_bin_id
	LEFT JOIN inventory_bundle bu ON bu.inventory_bundle_id = s.inventory_bundle_id
	LEFT JOIN inventory_slab p    ON p.inventory_slab_id = s.slab_parent_slab_id
	LEFT JOIN inventory_slab r    ON r.inventory_slab_id = s.slab_root_slab_id`

// unitRecoveredJoin adds, for a CONSUMED unit, how many offcuts came back from
// cutting it and their total area (rec.offcut_count, rec.recovered_area).
//
// The slab ledger is the source of truth: a 'recovered' row is written for an
// offcut that re-entered stock, by both the manual cut and a job's disposition,
// while an offcut too small to keep is born scrapped and never gets one. Reading
// the ledger — not the offcut's own flags — is what keeps the two paths, which
// stamp those flags differently, in agreement. The lateral term is skipped for
// any unit that has not been cut.
const unitRecoveredJoin = `
	LEFT JOIN LATERAL (
		SELECT COUNT(*) AS offcut_count, COALESCE(SUM(l.quantity_delta), 0) AS recovered_area
		FROM inventory_slab c
		JOIN inventory_slab_ledger l ON l.inventory_slab_id = c.inventory_slab_id AND l.event = 'recovered'
		WHERE s.slab_status = 'consumed'
		  AND c.slab_parent_slab_id = s.inventory_slab_id
		  AND c.slab_deleted_at IS NULL
	) rec ON TRUE`

// unitSelect is the canonical unit projection, joined to everything a yard
// screen needs to render a row without a second round trip.
//
// The fabrication and ledger joins are each at most one row per unit: a unit has
// one live job allocation (uq_fab_slab_live) and one 'consumed' and one
// 'scrapped' ledger row (uq_slab_ledger_consumed / _scrapped), so none of them
// can multiply the result.
const unitSelect = `
	SELECT s.inventory_slab_uuid, s.slab_serial, s.slab_unit_kind,
	       s.slab_vendor_id, s.slab_supplier_code, s.slab_barcode,
	       ii.inventory_item_uuid, ii.inventory_item_name, ii.inventory_item_sku,
	       s.warehouse_id, w.name,
	       b.inventory_bin_uuid, COALESCE(b.bin_path,''),
	       s.slab_bundle_id, bu.inventory_bundle_uuid, s.slab_block_id, s.slab_lot,
	       s.slab_length_mm, s.slab_width_mm, s.slab_thickness_mm, s.slab_area, s.slab_area_unit_id,
	       au.unit_code,
	       s.slab_form, s.slab_status,
	       p.inventory_slab_uuid, COALESCE(p.slab_serial,''),
	       r.inventory_slab_uuid, COALESCE(r.slab_serial,''),
	       s.slab_is_usable_remnant, s.slab_grade, s.slab_finish, s.slab_finish_id, s.slab_photo_key,
	       irc.item_receipt_uuid, COALESCE(irc.item_receipt_number,''),
	       fj.fabrication_job_uuid, COALESCE(fj.fabrication_job_number,''),
	       so.sales_order_uuid, COALESCE(so.sales_order_number,''), fjs.reserved_at,
	       lc.occurred_at, ls.occurred_at,
	       COALESCE(rec.offcut_count, 0), COALESCE(rec.recovered_area, 0),
	       s.slab_created_at, s.slab_updated_at` +
	unitFilterFrom + `
	LEFT JOIN item_receipt_line_unit iru ON iru.inventory_slab_id = s.inventory_slab_id
	LEFT JOIN item_receipt_line irl      ON irl.item_receipt_line_id = iru.item_receipt_line_id
	LEFT JOIN item_receipt irc           ON irc.item_receipt_id = irl.item_receipt_id
	                                    AND irc.item_receipt_deleted_at IS NULL
	LEFT JOIN fabrication_job_slab fjs   ON fjs.inventory_slab_id = s.inventory_slab_id
	                                    AND fjs.allocation_status IN ('reserved','consumed')
	LEFT JOIN fabrication_job fj         ON fj.fabrication_job_id = fjs.fabrication_job_id
	                                    AND fj.fabrication_job_deleted_at IS NULL
	LEFT JOIN sales_order so             ON so.sales_order_id = fj.sales_order_id
	                                    AND so.sales_order_deleted_at IS NULL
	LEFT JOIN inventory_slab_ledger lc   ON lc.inventory_slab_id = s.inventory_slab_id AND lc.event = 'consumed'
	LEFT JOIN inventory_slab_ledger ls   ON ls.inventory_slab_id = s.inventory_slab_id AND ls.event = 'scrapped'` +
	unitRecoveredJoin

func scanUnit(row pgx.Row) (*Unit, error) {
	var (
		u       Unit
		jobID   *string
		orderID *string
	)
	if err := row.Scan(
		&u.ID, &u.Serial, &u.Kind,
		&u.VendorID, &u.SupplierCode, &u.Barcode,
		&u.InventoryItemID, &u.InventoryItemName, &u.InventoryItemSKU,
		&u.WarehouseID, &u.WarehouseName,
		&u.BinID, &u.BinPath,
		&u.BundleID, &u.BundleUUID, &u.BlockID, &u.Lot,
		&u.LengthMM, &u.WidthMM, &u.ThicknessMM, &u.Area, &u.AreaUnitID,
		&u.AreaUnitCode,
		&u.Form, &u.Status,
		&u.ParentUnitID, &u.ParentSerial,
		&u.RootUnitID, &u.RootSerial,
		&u.IsUsableRemnant, &u.Grade, &u.Finish, &u.FinishID, &u.PhotoKey,
		&u.ReceiptID, &u.ReceiptNumber,
		&jobID, &u.Usage.JobNumber,
		&orderID, &u.Usage.SalesOrderNumber, &u.Usage.ReservedAt,
		&u.Usage.ConsumedAt, &u.Usage.ScrappedAt,
		&u.Usage.OffcutCount, &u.Usage.RecoveredArea,
		&u.CreatedAt, &u.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if jobID != nil {
		u.Usage.JobID = *jobID
	}
	if orderID != nil {
		u.Usage.SalesOrderID = *orderID
	}
	u.Usage.UsedArea = usedArea(u.Status, u.Area, u.Usage.RecoveredArea)
	return &u, nil
}

// usedArea is the stone a consumed unit lost to product and kerf: everything
// that was not recovered as an offcut. Zero for any unit still in stock (or
// scrapped), and clamped so a rounding sliver can never read as negative.
func usedArea(status string, area, recovered float64) float64 {
	if status != "consumed" {
		return 0
	}
	return roundTo(math.Max(area-recovered, 0), areaScale)
}

// GetUnit loads one live unit by uuid.
func GetUnit(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Unit, error) {
	u, err := scanUnit(pool.QueryRow(ctx, unitSelect+`
		WHERE s.inventory_slab_uuid = $1 AND s.slab_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get inventory unit: %w", err)
	}
	return u, nil
}

// unitRow is a unit's internal identity, resolved inside a transaction.
type unitRow struct {
	id          int
	itemID      int
	warehouseID int
	binID       *int
	bundleID    *int
	status      string
	area        float64
}

// unitByUUID resolves and locks a unit for update.
func unitByUUID(ctx context.Context, q pgxQuerier, uuid string, forUpdate bool) (unitRow, error) {
	sql := `SELECT inventory_slab_id, inventory_item_id, warehouse_id,
	               inventory_bin_id, inventory_bundle_id, slab_status, slab_area
	        FROM inventory_slab
	        WHERE inventory_slab_uuid = $1 AND slab_deleted_at IS NULL`
	if forUpdate {
		sql += " FOR UPDATE"
	}
	var u unitRow
	err := q.QueryRow(ctx, sql, uuid).Scan(
		&u.id, &u.itemID, &u.warehouseID, &u.binID, &u.bundleID, &u.status, &u.area)
	if errors.Is(err, pgx.ErrNoRows) {
		return unitRow{}, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return unitRow{}, ErrNotFound
		}
		return unitRow{}, fmt.Errorf("resolve inventory unit: %w", err)
	}
	return u, nil
}

// itemUnitInfo carries what the unit store needs to know about a unit's parent
// catalogue item to compute area correctly.
type itemUnitInfo struct {
	itemID       int
	unitID       int
	unitCode     string
	unitCategory string
	tracking     string
}

// resolveItemForUnit loads the parent item's unit of measure.
//
// The unit's area MUST be expressed in the item's own unit, because
// inventory_slab_ledger.quantity_delta is defined that way and feeds
// inventory_stock. Reading the item's unit here — rather than trusting
// slab_area_unit_id from the caller — is what keeps a SQM measurement from
// being ledgered against a SQFT item.
func resolveItemForUnit(ctx context.Context, q pgxQuerier, itemUUID string) (itemUnitInfo, error) {
	var info itemUnitInfo
	err := q.QueryRow(ctx, `
		SELECT ii.inventory_item_id, u.unit_id, u.unit_code, u.unit_category, ii.inventory_item_tracking
		FROM inventory_item ii
		JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_uuid = $1 AND ii.inventory_item_deleted_at IS NULL`,
		itemUUID).Scan(&info.itemID, &info.unitID, &info.unitCode, &info.unitCategory, &info.tracking)
	if errors.Is(err, pgx.ErrNoRows) {
		return itemUnitInfo{}, ClientError{Msg: "The referenced inventory item does not exist."}
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return itemUnitInfo{}, ClientError{Msg: "The referenced inventory item does not exist."}
		}
		return itemUnitInfo{}, fmt.Errorf("resolve unit item: %w", err)
	}
	return info, nil
}

// mapUnitWriteErr turns a constraint violation from a unit write into a
// client-facing 400 where one is warranted.
func mapUnitWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		return ClientError{Msg: "A live unit with that serial, barcode, or vendor and supplier code already exists."}
	case isFKViolation(err):
		return ClientError{Msg: "An invalid item, vendor, location, bin or bundle was referenced."}
	case isCheckViolation(err):
		return ClientError{Msg: "One or more unit values are out of range."}
	}
	return fmt.Errorf("%s inventory unit: %w", verb, err)
}

// writeUnitHistory appends one row to inventory_unit_history, the OPERATIONAL
// trail for a physical piece.
//
// Deliberately separate from inventory_slab_ledger, which is the FINANCIAL
// record: bin moves, re-grades and photo swaps change no quantity, so they must
// not touch the ledger. Writing a zero-delta row there would collide with its
// once-only partial indexes and pollute the stock audit with non-events.
func writeUnitHistory(ctx context.Context, tx pgx.Tx, unitID int, action, field, oldVal, newVal string,
	fromBin, toBin *int, reasonID *int, note string, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_unit_history (
			inventory_slab_id, history_action, history_field,
			history_old_value, history_new_value,
			from_bin_id, to_bin_id, inventory_reason_id, history_note, history_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		unitID, action, field, oldVal, newVal,
		fromBin, toBin, reasonID, note, nullableInt(actorEmployeeID),
	); err != nil {
		return fmt.Errorf("write inventory unit history: %w", err)
	}
	return nil
}
