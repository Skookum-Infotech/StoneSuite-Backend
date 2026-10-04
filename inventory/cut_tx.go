package inventory

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// CutTxResult identifies inventory changes pending in the caller's transaction.
type CutTxResult struct {
	RemnantIDs    []string
	ConsumedArea  float64
	RecoveredArea float64
	LostArea      float64
}

// CutUnitTx consumes an unreserved unit and creates remnants atomically with
// the caller's other writes. The caller must roll back on any error and owns
// commit. Reserved fabrication units remain blocked until run validation exists.
func CutUnitTx(ctx context.Context, tx pgx.Tx, uuid string, in CutInput, actorEmployeeID int) (CutTxResult, error) {
	return cutUnitTx(ctx, tx, uuid, in, nil, actorEmployeeID)
}

func cutUnitTx(ctx context.Context, tx pgx.Tx, uuid string, in CutInput, destinations []string, actorEmployeeID int) (CutTxResult, error) {
	for i := range in.Remnants {
		if strings.TrimSpace(in.Remnants[i].Serial) == "" {
			return CutTxResult{}, ClientError{Msg: "Every remnant needs a serial."}
		}
		if in.Remnants[i].LengthMM <= 0 || in.Remnants[i].WidthMM <= 0 {
			return CutTxResult{}, ClientError{Msg: "Every remnant needs a positive length and width."}
		}
	}

	if err := RequireInspectedTx(ctx, tx, uuid); err != nil {
		return CutTxResult{}, err
	}
	p, err := lockParentForCut(ctx, tx, uuid)
	if err != nil {
		return CutTxResult{}, err
	}
	switch p.status {
	case "consumed":
		return CutTxResult{}, ClientError{Msg: "This unit has already been consumed."}
	case "scrapped":
		return CutTxResult{}, ClientError{Msg: "A scrapped unit cannot be cut."}
	case "reserved":
		// The slab is committed to a fabrication job, which has its own consume
		// path. Cutting it here would deduct the same stone twice.
		return CutTxResult{}, ClientError{Msg: "This unit is reserved for a job. Release the reservation before cutting it."}
	case StatusInTransit:
		// It is physically on a truck. Its area has already left the source
		// warehouse's stock, so a cut here would consume it a second time.
		return CutTxResult{}, ClientError{Msg: "This unit is in transit. Receive its transfer before cutting it."}
	}
	// Same rule as MoveUnitToBin: a sealed bundle is physically banded, and the
	// slab has to come off the pallet before a saw touches it. Cross-row, so no
	// CHECK can express it.
	if p.bundleID != nil {
		var bundleStatus string
		if err := tx.QueryRow(ctx, `
			SELECT bundle_status FROM inventory_bundle WHERE inventory_bundle_id = $1`,
			*p.bundleID).Scan(&bundleStatus); err != nil {
			return CutTxResult{}, fmt.Errorf("read bundle status: %w", err)
		}
		if bundleStatus == BundleSealed {
			return CutTxResult{}, ClientError{Msg: "This unit is in a sealed bundle. Break the bundle before cutting it."}
		}
	}

	// Areas are computed from the millimetres into the ITEM's unit, never taken
	// from the caller — the same rule as receipt.
	childAreas := make([]float64, len(in.Remnants))
	for i, r := range in.Remnants {
		a, err := AreaFor(r.LengthMM, r.WidthMM, p.unitCode, p.unitCat)
		if err != nil {
			return CutTxResult{}, err
		}
		childAreas[i] = a
	}
	plan, err := PlanCut(p.area, childAreas)
	if err != nil {
		return CutTxResult{}, err
	}

	bins := make([]*int, len(in.Remnants))
	for i := range bins {
		bins[i] = p.binID
	}
	if destinations != nil {
		if err := checkBinMoveNotFrozen(ctx, tx, p.warehouseID, p.binID, p.binID); err != nil {
			return CutTxResult{}, err
		}
		for i, destination := range destinations {
			bin, err := cutDestination(ctx, tx, destination, p.warehouseID, p.binID)
			if err != nil {
				return CutTxResult{}, err
			}
			bins[i] = bin
		}
	}

	// The parent leaves stock in full.
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab SET slab_status = 'consumed', slab_updated_at = NOW(), slab_updated_by = $2
		WHERE inventory_slab_id = $1`, p.id, nullableInt(actorEmployeeID)); err != nil {
		return CutTxResult{}, fmt.Errorf("mark unit consumed: %w", err)
	}
	if err := SlabLedgerAndStock(ctx, tx, p.id, p.itemID, p.warehouseID,
		EventConsumed, -p.area, nil, actorEmployeeID); err != nil {
		return CutTxResult{}, err
	}

	// Every remnant descends from the ORIGINAL slab, not from its immediate
	// parent, so recall ("every piece from vendor lot X") stays one indexed
	// equality rather than a recursive walk.
	rootID := p.id
	if p.rootID != nil {
		rootID = *p.rootID
	}

	var (
		recovered float64
		outUUIDs  []string
	)
	for i, r := range in.Remnants {
		usable := IsUsableRemnant(r.LengthMM, r.WidthMM, in.MinUsableLengthMM, in.MinUsableWidthMM)
		// An unusable offcut is recorded so the cut has a complete history, but
		// it is born scrapped and never enters available stock — so it cannot
		// inflate on-hand area or clutter the remnant picker.
		status := "available"
		if !usable {
			status = "scrapped"
		}
		childParent := p
		childParent.binID = bins[i]
		childUUID, childID, err := insertRemnant(ctx, tx, childParent, r, childAreas[i], rootID, usable, status, actorEmployeeID)
		if err != nil {
			return CutTxResult{}, err
		}
		outUUIDs = append(outUUIDs, childUUID)

		if usable {
			if err := SlabLedgerAndStock(ctx, tx, childID, p.itemID, p.warehouseID,
				EventRecovered, childAreas[i], nil, actorEmployeeID); err != nil {
				return CutTxResult{}, err
			}
			recovered += childAreas[i]
		}
		if err := writeUnitHistory(ctx, tx, childID, "remnant_created", "parentSerial", "", r.Serial,
			nil, bins[i], in.ReasonID, in.Note, actorEmployeeID); err != nil {
			return CutTxResult{}, err
		}
	}

	// The shortfall is recorded operationally, NOT as a ledger row: consuming
	// the parent in full while recovering only the remnants has already removed
	// it from stock. A 'scrapped' row here would deduct the kerf a second time.
	lost := roundTo(p.area-recovered, areaScale)
	if err := writeUnitHistory(ctx, tx, p.id, "cut", "area",
		fmt.Sprintf("%.3f", p.area), fmt.Sprintf("%.3f recovered, %.3f lost", recovered, lost),
		p.binID, nil, in.ReasonID, in.Note, actorEmployeeID); err != nil {
		return CutTxResult{}, err
	}

	return CutTxResult{RemnantIDs: outUUIDs, ConsumedArea: plan.ParentArea,
		RecoveredArea: roundTo(recovered, areaScale), LostArea: lost}, nil
}
