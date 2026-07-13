package salesorder

import "testing"

// TestLineStatus covers the derived per-line fulfillment label (AD-9,
// schema.org OrderItem.orderItemStatus).
func TestLineStatus(t *testing.T) {
	cases := []struct {
		name      string
		fulfilled float64
		quantity  float64
		want      string
	}{
		{"nothing fulfilled", 0, 10, "open"},
		{"negative treated as open", -1, 10, "open"},
		{"partially fulfilled", 4, 10, "partial"},
		{"exactly fulfilled", 10, 10, "filled"},
		{"over fulfilled clamps to filled", 12, 10, "filled"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := lineStatus(c.fulfilled, c.quantity); got != c.want {
				t.Errorf("lineStatus(%v, %v) = %q, want %q", c.fulfilled, c.quantity, got, c.want)
			}
		})
	}
}
