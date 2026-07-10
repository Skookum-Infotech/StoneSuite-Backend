package salesorder

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "SORD-000001", 42: "SORD-000042", 1234567: "SORD-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
