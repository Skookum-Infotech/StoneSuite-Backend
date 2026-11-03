package purchaseorder

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOnOrderLine_ThicknessMM(t *testing.T) {
	inches := 0.75
	tests := []struct {
		name    string
		line    onOrderLine
		want    float64
		wantErr bool
	}{
		{name: "the line's own thickness wins, converted from its unit",
			line: onOrderLine{thickness: &inches, unit: "in", itemThickness: 30}, want: 19.05},
		{name: "in centimetres", line: onOrderLine{thickness: floatRef(3), unit: "cm"}, want: 30},
		{name: "falls back to the item's, already millimetres",
			line: onOrderLine{unit: "in", itemThickness: 20}, want: 20},
		{name: "neither states one", line: onOrderLine{unit: "in"}, want: 0},
		{name: "an unknown unit on the line is refused", line: onOrderLine{thickness: &inches, unit: "yd"}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.line.thicknessMM()
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, IsClientError(err))
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}
}

func floatRef(v float64) *float64 { return &v }
