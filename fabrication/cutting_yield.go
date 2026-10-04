package fabrication

import (
	"fmt"
	"math"

	"stonesuite-backend/inventory"
)

const (
	cuttingAreaPrecision = 0.001
	// inventory_slab.slab_area and ledger quantities are DECIMAL(14,3).
	maxCuttingArea = 99999999999.999
)

// cuttingYield separates finished work from recovered stock. Waste is reporting
// only: posting it separately would deduct the same material twice.
type cuttingYield struct {
	FinishedArea     float64
	RecoveredArea    float64
	WasteArea        float64
	NetStockDecrease float64
}

// planCuttingYield validates one parent in its inventory item's area unit.
// Completion must separately match outputs to selected pieces and verify remnant
// destinations. Disposition of failed outputs is a different action.
func planCuttingYield(parentArea float64, unitCode string, outputs, remnants []MeasuredPiece) (cuttingYield, error) {
	if !positiveFinite(parentArea) || parentArea > maxCuttingArea {
		return cuttingYield{}, ClientError{Msg: "Cutting material must have a positive finite recorded area."}
	}
	if len(outputs) == 0 {
		return cuttingYield{}, ClientError{Msg: "Record the finished pieces before completing cutting."}
	}
	finished, err := measuredCuttingArea(outputs, unitCode)
	if err != nil {
		return cuttingYield{}, err
	}
	recovered, err := measuredCuttingArea(remnants, unitCode)
	if err != nil {
		return cuttingYield{}, err
	}
	// Never allow the tolerance to manufacture recovered stock.
	if recovered > parentArea {
		return cuttingYield{}, ClientError{Msg: "Recovered remnants exceed the source material's area."}
	}
	total := finished + recovered
	if math.IsInf(total, 0) || math.IsNaN(total) || total > maxCuttingArea {
		return cuttingYield{}, ClientError{Msg: "The measured cutting areas are out of range."}
	}
	// Work at the same precision as inventory's persisted quantities. Rounding
	// the difference avoids rejecting exactly one precision increment due to FP.
	difference := roundCuttingArea(total - parentArea)
	if difference > cuttingAreaPrecision {
		return cuttingYield{}, ClientError{Msg: "Finished pieces and remnants exceed the source material's area."}
	}
	return cuttingYield{FinishedArea: finished, RecoveredArea: recovered,
		WasteArea:        math.Max(0, roundCuttingArea(parentArea-total)),
		NetStockDecrease: roundCuttingArea(parentArea - recovered)}, nil
}

func measuredCuttingArea(pieces []MeasuredPiece, unitCode string) (float64, error) {
	var total float64
	for _, piece := range pieces {
		if !positiveFinite(piece.LengthMM) || !positiveFinite(piece.WidthMM) || !positiveFinite(piece.ThicknessMM) {
			return 0, ClientError{Msg: "Every finished piece and remnant needs positive finite dimensions."}
		}
		area, err := inventory.AreaFor(piece.LengthMM, piece.WidthMM, unitCode, inventory.UnitCategoryArea)
		if err != nil {
			return 0, fmt.Errorf("calculate cutting measurement: %w", err)
		}
		if !positiveFinite(area) || area > maxCuttingArea {
			return 0, ClientError{Msg: "A measured piece has no representable area in the material's unit."}
		}
		total += area
		if math.IsInf(total, 0) || math.IsNaN(total) || total > maxCuttingArea {
			return 0, ClientError{Msg: "The measured cutting areas are out of range."}
		}
	}
	return roundCuttingArea(total), nil
}

func roundCuttingArea(value float64) float64 {
	return math.Round(value/cuttingAreaPrecision) * cuttingAreaPrecision
}
