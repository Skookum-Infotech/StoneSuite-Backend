package fabrication

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestPurchaseRequirementDescription(t *testing.T) {
	for _, tc := range []struct{ name, finish, want string }{
		{"specified finish", "Honed", "Finish: Honed"},
		{"unspecified finish", " ", "Finish: Confirm with supplier"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := purchaseRequirementDescription(TemplateLine{Finish: tc.finish, Pieces: []MeasuredPiece{{Name: "Island", LengthMM: 1500.5, WidthMM: 600, ThicknessMM: 30}, {Name: "Splash", LengthMM: 500, WidthMM: 100, ThicknessMM: 20}}}, 3)
			assert.Contains(t, result, "revision 3")
			assert.Contains(t, result, tc.want)
			assert.Contains(t, result, "Island: 1500.5 × 600 × 30 mm")
			assert.Contains(t, result, "Splash: 500 × 100 × 20 mm")
			assert.Contains(t, result, "not slab order sizes")
		})
	}
}
