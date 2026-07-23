package purchaseorder

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		id   int64
		want string
	}{
		{1, "PORD-000001"},
		{42, "PORD-000042"},
		{999999, "PORD-999999"},
		{1000000, "PORD-1000000"}, // grows past the pad, never truncates
	}
	for _, tt := range tests {
		if got := FormatNumber(tt.id); got != tt.want {
			t.Fatalf("FormatNumber(%d) = %q, want %q", tt.id, got, tt.want)
		}
	}
}
