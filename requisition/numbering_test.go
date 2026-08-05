package requisition

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		serialID int64
		want     string
	}{
		{1, "REQN-000001"},
		{42, "REQN-000042"},
		{123456, "REQN-123456"},
		{1234567, "REQN-1234567"},
	}
	for _, tt := range tests {
		if got := FormatNumber(tt.serialID); got != tt.want {
			t.Errorf("FormatNumber(%d) = %q, want %q", tt.serialID, got, tt.want)
		}
	}
}
