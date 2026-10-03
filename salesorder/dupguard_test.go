package salesorder

import "testing"

func TestNormalizeDocNumber(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"PO-0042", "42"},
		{"po 42", "42"},
		{"#00123", "123"},
		{"INV-000123", "123"},
		{"000", "0"},
		{"007A", "7a"},
		{"AB-77", "ab77"},
	}
	for _, c := range cases {
		if got := NormalizeDocNumber(c.in); got != c.want {
			t.Errorf("NormalizeDocNumber(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
