package estimate

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		name     string
		serialID int64
		want     string
	}{
		{"single digit", 1, "ESTM-000001"},
		{"three digits", 123, "ESTM-000123"},
		{"six digits exact", 654321, "ESTM-654321"},
		{"seven digits not truncated", 1234567, "ESTM-1234567"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatNumber(tt.serialID); got != tt.want {
				t.Fatalf("FormatNumber(%d) = %q, want %q", tt.serialID, got, tt.want)
			}
		})
	}
}
