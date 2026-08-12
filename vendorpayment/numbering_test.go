package vendorpayment

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "VPAY-000001", 42: "VPAY-000042", 1234567: "VPAY-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
