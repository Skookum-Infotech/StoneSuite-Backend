package purchaseorder

import "testing"

func TestReceivedPercent(t *testing.T) {
	tests := []struct {
		name  string
		lines []LineReceipt
		want  int
	}{
		{"no lines", nil, 0},
		{"nothing received", []LineReceipt{{10, 0}, {5, 0}}, 0},
		{"one line half received", []LineReceipt{{10, 5}}, 50},
		{"fully received", []LineReceipt{{10, 10}, {5, 5}}, 100},
		{"lines count equally whatever their quantity", []LineReceipt{{1000, 1000}, {1, 0}}, 50},
		{"average of partial lines", []LineReceipt{{10, 10}, {10, 5}, {10, 0}}, 50},
		{"over-receipt is capped per line", []LineReceipt{{10, 20}, {10, 0}}, 50},
		{"rounds to a whole percent", []LineReceipt{{3, 1}}, 33},
		{"rounds half up", []LineReceipt{{8, 1}}, 13},
		{"nearly complete never reads 100", []LineReceipt{{1000, 999}}, 99},
		{"barely started never reads 0", []LineReceipt{{1000, 1}}, 1},
		{"zero-quantity lines ignored", []LineReceipt{{0, 0}, {10, 10}}, 100},
		{"only zero-quantity lines", []LineReceipt{{0, 0}}, 0},
		{"negative received counts as nothing", []LineReceipt{{10, -4}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ReceivedPercent(tt.lines); got != tt.want {
				t.Fatalf("ReceivedPercent(%+v) = %d, want %d", tt.lines, got, tt.want)
			}
		})
	}
}
