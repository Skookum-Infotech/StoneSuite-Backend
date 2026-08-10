package vendorbill

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		id   int64
		want string
	}{
		{1, "VBIL-000001"},
		{42, "VBIL-000042"},
		{999999, "VBIL-999999"},
		{1000000, "VBIL-1000000"}, // grows past the pad, never truncates
	}
	for _, tt := range tests {
		if got := FormatNumber(tt.id); got != tt.want {
			t.Fatalf("FormatNumber(%d) = %q, want %q", tt.id, got, tt.want)
		}
	}
}
