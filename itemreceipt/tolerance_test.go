package itemreceipt

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestAllowedCeiling(t *testing.T) {
	tests := []struct {
		name    string
		ordered float64
		want    float64
	}{
		{"100 ordered gets 5% headroom", 100, 105},
		{"200 ordered gets 5% headroom", 200, 210},
		{"fractional ordered", 10.5, 11.025},
		{"zero ordered has no ceiling", 0, 0},
		{"negative ordered has no ceiling", -5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, AllowedCeiling(tt.ordered), 1e-9)
		})
	}
}

func TestWithinTolerance(t *testing.T) {
	tests := []struct {
		name        string
		ordered     float64
		alreadyRecv float64
		incoming    float64
		want        bool
	}{
		{"exact quantity", 100, 0, 100, true},
		{"under quantity", 100, 0, 50, true},
		{"exactly at the ceiling", 100, 0, 105, true},
		{"one unit past the ceiling", 100, 0, 106, false},
		{"small over-delivery inside tolerance", 100, 0, 103, true},

		// The check is cumulative: no single receipt exceeds the order, but
		// together the third one does.
		{"three partials, still inside", 100, 60, 40, true},
		{"three partials, third trips it", 100, 100, 10, false},
		{"second receipt inside tolerance", 100, 100, 5, true},

		{"nothing incoming is always fine", 100, 100, 0, true},
		{"negative incoming is always fine", 100, 100, -5, true},

		// A free-text PO line carries no ordered quantity, so there is nothing
		// to measure a delivery against — a human has to approve it.
		{"zero ordered, something arrives", 0, 0, 1, false},
		{"zero ordered, nothing arrives", 0, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, WithinTolerance(tt.ordered, tt.alreadyRecv, tt.incoming))
		})
	}
}
