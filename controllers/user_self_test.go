package controllers

import (
	"strings"
	"testing"
)

func TestNormalizeProfileName(t *testing.T) {
	tests := []struct {
		name        string
		in          string
		wantName    string
		wantInvalid string
	}{
		{"plain", "Ada Lovelace", "Ada Lovelace", ""},
		{"trims surrounding whitespace", "  Ada Lovelace \t", "Ada Lovelace", ""},
		{"single name is allowed", "Prince", "Prince", ""},
		{"empty", "", "", "Name is required."},
		{"whitespace only", "   \t ", "", "Name is required."},
		{"exactly the limit", strings.Repeat("a", maxProfileNameLen), strings.Repeat("a", maxProfileNameLen), ""},
		{"one over the limit", strings.Repeat("a", maxProfileNameLen+1), "", "Name is too long."},
		{
			// 120 two-byte characters is 240 bytes: it must pass, proving the
			// cap counts characters (the column's unit), not bytes.
			"multibyte at the limit", strings.Repeat("é", maxProfileNameLen), strings.Repeat("é", maxProfileNameLen), "",
		},
		{"multibyte over the limit", strings.Repeat("é", maxProfileNameLen+1), "", "Name is too long."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotName, gotInvalid := normalizeProfileName(tc.in)
			if gotName != tc.wantName || gotInvalid != tc.wantInvalid {
				t.Fatalf("normalizeProfileName(%q) = (%q, %q), want (%q, %q)",
					tc.in, gotName, gotInvalid, tc.wantName, tc.wantInvalid)
			}
		})
	}
}
