package cashtransfer

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "CTRF-000001", 42: "CTRF-000042", 1234567: "CTRF-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
