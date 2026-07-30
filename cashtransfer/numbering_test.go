package cashtransfer

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "JE-000001", 42: "JE-000042", 1234567: "JE-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
