package userstore

import "testing"

func TestSplitName(t *testing.T) {
	cases := []struct {
		name     string
		fullName string
		first    string
		last     string
	}{
		{"empty", "", "", ""},
		{"whitespace only", "   ", "", ""},
		{"single token", "Cher", "Cher", ""},
		{"two tokens", "Ada Lovelace", "Ada", "Lovelace"},
		{"three tokens keeps remainder in last", "Mary Jane Watson", "Mary", "Jane Watson"},
		{"leading/trailing whitespace trimmed", "  Grace Hopper  ", "Grace", "Hopper"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first, last := splitName(tc.fullName)
			if first != tc.first || last != tc.last {
				t.Errorf("splitName(%q) = (%q, %q), want (%q, %q)", tc.fullName, first, last, tc.first, tc.last)
			}
		})
	}
}
