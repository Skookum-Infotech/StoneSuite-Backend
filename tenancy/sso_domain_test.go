package tenancy

import "testing"

// TestNormalizeEmailDomain exercises NormalizeEmailDomain's edge cases: bare
// domain vs full email vs empty/whitespace input, matching the table-driven
// convention used elsewhere in this package (see tenant_test.go).
func TestNormalizeEmailDomain(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare domain unchanged", "contoso.com", "contoso.com"},
		{"full email reduces to domain", "user@contoso.com", "contoso.com"},
		{"mixed case lowercased", "User@Contoso.COM", "contoso.com"},
		{"bare domain mixed case lowercased", "Contoso.COM", "contoso.com"},
		{"surrounding whitespace trimmed", "  user@contoso.com  ", "contoso.com"},
		{"empty input", "", ""},
		{"whitespace-only input", "   ", ""},
		{"bare @ only", "@", ""},
		{"trailing @ with nothing after", "user@", ""},
		{"leading @ with domain only", "@contoso.com", "contoso.com"},
		{"multiple @ takes the segment after the last one", "a@b@contoso.com", "contoso.com"},
		{"whitespace between @ and domain trimmed", "user@  contoso.com", "contoso.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeEmailDomain(tc.in); got != tc.want {
				t.Errorf("NormalizeEmailDomain(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
