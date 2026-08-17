package expense

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		serialID int64
		want     string
	}{
		{1, "EXPN-000001"},
		{42, "EXPN-000042"},
		{123456, "EXPN-123456"},
		{1234567, "EXPN-1234567"},
	}
	for _, tt := range tests {
		if got := FormatNumber(tt.serialID); got != tt.want {
			t.Errorf("FormatNumber(%d) = %q, want %q", tt.serialID, got, tt.want)
		}
	}
}
