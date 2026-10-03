package fabrication

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"stonesuite-backend/inventory"
)

func TestPlanCuttingYield(t *testing.T) {
	piece := func(length, width float64) MeasuredPiece {
		return MeasuredPiece{LengthMM: length, WidthMM: width, ThicknessMM: 30}
	}
	tests := []struct {
		name, unit                 string
		parent                     float64
		outputs, remnants          []MeasuredPiece
		finished, recovered, waste float64
		invalid                    bool
	}{
		{name: "square metres", unit: inventory.UnitCodeSquareM, parent: 10, outputs: []MeasuredPiece{piece(2000, 3000)}, remnants: []MeasuredPiece{piece(1000, 3000)}, finished: 6, recovered: 3, waste: 1},
		{name: "square feet", unit: inventory.UnitCodeSquareFoot, parent: 10, outputs: []MeasuredPiece{piece(609.6, 914.4)}, remnants: []MeasuredPiece{piece(304.8, 914.4)}, finished: 6, recovered: 3, waste: 1},
		{name: "zero remnants", unit: inventory.UnitCodeSquareM, parent: 10, outputs: []MeasuredPiece{piece(2000, 3000)}, finished: 6, waste: 4},
		{name: "precision tolerance", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{piece(1000, 1001)}, finished: 1.001},
		{name: "impossible outputs", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{piece(1000, 1002)}, invalid: true},
		{name: "combined excess", unit: inventory.UnitCodeSquareM, parent: 10, outputs: []MeasuredPiece{piece(2000, 4000)}, remnants: []MeasuredPiece{piece(1000, 3000)}, invalid: true},
		{name: "recovery never creates stock", unit: inventory.UnitCodeSquareM, parent: 1, remnants: []MeasuredPiece{piece(1000, 1001)}, invalid: true},
		{name: "no finished outputs", unit: inventory.UnitCodeSquareM, parent: 1, invalid: true},
		{name: "nan parent", unit: inventory.UnitCodeSquareM, parent: math.NaN(), outputs: []MeasuredPiece{piece(1, 1)}, invalid: true},
		{name: "infinite dimension", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{piece(math.Inf(1), 1)}, invalid: true},
		{name: "nan thickness", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{{LengthMM: 100, WidthMM: 100, ThicknessMM: math.NaN()}}, invalid: true},
		{name: "tiny rounded output", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{piece(1, 1)}, invalid: true},
		{name: "zero remnant dimension", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{piece(500, 500)}, remnants: []MeasuredPiece{piece(0, 1)}, invalid: true},
		{name: "unsupported unit", unit: "EA", parent: 1, outputs: []MeasuredPiece{piece(100, 100)}, invalid: true},
		{name: "unrepresentable parent", unit: inventory.UnitCodeSquareM, parent: math.MaxFloat64, outputs: []MeasuredPiece{piece(1000, 1000)}, invalid: true},
		{name: "negative measurement", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{piece(-100, 100)}, invalid: true},
		{name: "multiple individually rounded remnants", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{piece(500, 1000)}, remnants: []MeasuredPiece{piece(333, 100), piece(333, 100)}, finished: 0.5, recovered: 0.066, waste: 0.434},
		{name: "overflow", unit: inventory.UnitCodeSquareM, parent: 1, outputs: []MeasuredPiece{piece(math.MaxFloat64, math.MaxFloat64)}, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planCuttingYield(tt.parent, tt.unit, tt.outputs, tt.remnants)
			if tt.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.InDelta(t, tt.finished, got.FinishedArea, 0.0000001)
			require.InDelta(t, tt.recovered, got.RecoveredArea, 0.0000001)
			require.InDelta(t, tt.waste, got.WasteArea, 0.0000001)
			require.InDelta(t, tt.parent-tt.recovered, got.NetStockDecrease, 0.0000001)
		})
	}
}
