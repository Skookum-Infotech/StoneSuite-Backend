package vendorcredit

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		id   int64
		want string
	}{
		{1, "VCR-000001"},
		{42, "VCR-000042"},
		{1000000, "VCR-1000000"}, // grows past the pad, never truncates
	}
	for _, tt := range tests {
		if got := FormatNumber(tt.id); got != tt.want {
			t.Fatalf("FormatNumber(%d) = %q, want %q", tt.id, got, tt.want)
		}
	}
}
