package purchaseorder

import "testing"

func TestRollupReceiptStatus(t *testing.T) {
	tests := []struct {
		name  string
		lines []LineReceipt
		want  string
	}{
		{"no lines", nil, ""},
		{"nothing received", []LineReceipt{{10, 0}, {5, 0}}, ""},
		{"some received", []LineReceipt{{10, 4}, {5, 0}}, "PART"},
		{"one line partially received", []LineReceipt{{10, 9.5}}, "PART"},
		{"all fully received", []LineReceipt{{10, 10}, {5, 5}}, "RCVD"},
		{"over-receipt still counts as received", []LineReceipt{{10, 12}}, "RCVD"},
		{"zero-quantity lines ignored", []LineReceipt{{0, 0}, {10, 10}}, "RCVD"},
		{"only zero-quantity lines", []LineReceipt{{0, 0}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RollupReceiptStatus(tt.lines); got != tt.want {
				t.Fatalf("RollupReceiptStatus(%+v) = %q, want %q", tt.lines, got, tt.want)
			}
		})
	}
}
