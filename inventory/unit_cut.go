package inventory

// unit_cut.go — cutting a slab into pieces.
//
// The parent leaves stock in full and each remnant the shop keeps comes back at
// its own measured area. The difference — saw kerf, plus everything cut into
// finished countertop that is no longer inventory — is loss, and it needs NO
// ledger row of its own: it is already expressed by consuming more than is
// recovered. See the worked example on PlanCut.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// CutPiece is one offcut the shop is keeping. Pieces that are cut into finished
// product are NOT listed — they leave inventory with the parent.
type CutPiece struct {
	Serial   string  `json:"serial"`
	Barcode  string  `json:"barcode"`
	LengthMM float64 `json:"lengthMm"`
	WidthMM  float64 `json:"widthMm"`
	Grade    string  `json:"grade"`
}

// CutInput describes a cut.
type CutInput struct {
	// Remnants being kept. May be empty: a slab consumed entirely into finished
	// product yields nothing back.
	Remnants []CutPiece `json:"remnants"`
	// Minimum useful rectangle. A kept piece smaller than this is recorded but
	// flagged unusable and never enters available stock, which is what stops
	// the yard filling with worthless offcuts that inflate on-hand area.
	// Zero means "no policy — everything kept is usable".
	MinUsableLengthMM float64 `json:"minUsableLengthMm"`
	MinUsableWidthMM  float64 `json:"minUsableWidthMm"`
	ReasonID          *int    `json:"reasonId"`
	Note              string  `json:"note"`
}

// CutResult reports what the cut did to stock.
type CutResult struct {
	Parent   *Unit  `json:"parent"`
	Remnants []Unit `json:"remnants"`
	// ConsumedArea left stock; RecoveredArea came back; LostArea is the
	// difference — kerf plus finished product. LostArea is REPORTING ONLY and
	// has no ledger row.
	ConsumedArea  float64 `json:"consumedArea"`
	RecoveredArea float64 `json:"recoveredArea"`
	LostArea      float64 `json:"lostArea"`
}

// parentForCut is the parent's full state, read under lock.
type parentForCut struct {
	id          int
	itemID      int
	warehouseID int
	binID       *int
	bundleID    *int
	rootID      *int
	status      string
	area        float64
	thicknessMM float64
	areaUnitID  int
	finishID    *int
	finish      string
	vendorID    *int
	lot         string
	blockID     string
	unitCode    string
	unitCat     string
}

// CutUnit consumes a unit and mints the remnants kept from it.
func CutUnit(ctx context.Context, pool *pgxpool.Pool, uuid string, in CutInput, actorEmployeeID int) (*CutResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin cut unit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := CutUnitTx(ctx, tx, uuid, in, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit cut unit: %w", err)
	}

	parent, err := GetUnit(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	out := &CutResult{
		Parent:        parent,
		Remnants:      []Unit{},
		ConsumedArea:  result.ConsumedArea,
		RecoveredArea: result.RecoveredArea,
		LostArea:      result.LostArea,
	}
	for _, u := range result.RemnantIDs {
		child, err := GetUnit(ctx, pool, u)
		if err != nil {
			return nil, err
		}
		out.Remnants = append(out.Remnants, *child)
	}
	return out, nil
}

func lockParentForCut(ctx context.Context, tx pgx.Tx, uuid string) (parentForCut, error) {
	var p parentForCut
	err := tx.QueryRow(ctx, `
		SELECT s.inventory_slab_id, s.inventory_item_id, s.warehouse_id,
		       s.inventory_bin_id, s.inventory_bundle_id, s.slab_root_slab_id,
		       s.slab_status, s.slab_area, s.slab_thickness_mm, s.slab_area_unit_id,
		       s.slab_finish_id, s.slab_finish, s.slab_vendor_id, s.slab_lot, s.slab_block_id,
		       u.unit_code, u.unit_category
		FROM inventory_slab s
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		JOIN lkp_unit u        ON u.unit_id = ii.inventory_item_unit_id
		WHERE s.inventory_slab_uuid = $1 AND s.slab_deleted_at IS NULL
		FOR UPDATE OF s`, uuid).Scan(
		&p.id, &p.itemID, &p.warehouseID, &p.binID, &p.bundleID, &p.rootID,
		&p.status, &p.area, &p.thicknessMM, &p.areaUnitID,
		&p.finishID, &p.finish, &p.vendorID, &p.lot, &p.blockID,
		&p.unitCode, &p.unitCat)
	if errors.Is(err, pgx.ErrNoRows) {
		return parentForCut{}, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return parentForCut{}, ErrNotFound
		}
		return parentForCut{}, fmt.Errorf("lock unit for cut: %w", err)
	}
	return p, nil
}

// insertRemnant mints one offcut, inheriting the parent's item, location and
// provenance. slab_form is 'cut' and slab_parent_slab_id is set together, which
// is what chk_slab_form_parent requires.
func insertRemnant(ctx context.Context, tx pgx.Tx, p parentForCut, r CutPiece,
	area float64, rootID int, usable bool, status string, actorEmployeeID int) (string, int, error) {
	var (
		childUUID string
		childID   int
	)
	err := tx.QueryRow(ctx, `
		INSERT INTO inventory_slab (
			slab_serial, slab_unit_kind, slab_barcode, slab_vendor_id,
			inventory_item_id, warehouse_id, inventory_bin_id,
			slab_block_id, slab_lot,
			slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area, slab_area_unit_id,
			slab_form, slab_parent_slab_id, slab_root_slab_id, slab_status,
			slab_is_usable_remnant, slab_grade, slab_finish, slab_finish_id,
			slab_received_at, slab_created_by)
		VALUES ($1,$2,$3,$4, $5,$6,$7, $8,$9, $10,$11,$12,$13,$14,
			'cut',$15,$16,$17, $18,$19,$20,$21, CURRENT_DATE,$22)
		RETURNING inventory_slab_uuid, inventory_slab_id`,
		r.Serial, UnitKindRemnant, r.Barcode, p.vendorID,
		p.itemID, p.warehouseID, p.binID,
		p.blockID, p.lot,
		r.LengthMM, r.WidthMM, p.thicknessMM, area, p.areaUnitID,
		p.id, rootID, status,
		usable, r.Grade, p.finish, p.finishID,
		nullableInt(actorEmployeeID),
	).Scan(&childUUID, &childID)
	if err != nil {
		return "", 0, mapUnitWriteErr(err, "insert remnant from")
	}
	return childUUID, childID, nil
}
