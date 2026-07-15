package payment

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "PYMT-000001", 42: "PYMT-000042", 1234567: "PYMT-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
