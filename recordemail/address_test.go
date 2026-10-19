package recordemail

import (
	"strings"
	"testing"
)

func TestNewTokenShapeAndUniqueness(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		tok, err := NewToken()
		if err != nil {
			t.Fatalf("NewToken: %v", err)
		}
		if !tokenPattern.MatchString(tok) {
			t.Fatalf("token %q does not match the reply-address pattern", tok)
		}
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
	}
}

func TestAddressRoundTrip(t *testing.T) {
	tok, _ := NewToken()
	addr := Address(tok, "reply.example.com")
	got, ok := TokenFromAddresses([]string{addr}, "reply.example.com")
	if !ok || got != tok {
		t.Fatalf("round trip: got (%q, %v), want (%q, true)", got, ok, tok)
	}
}

func TestTokenFromAddresses(t *testing.T) {
	const domain = "reply.example.com"
	const tok = "abcdefghijklmnopqrst" // 20 chars of a-z

	tests := []struct {
		name  string
		addrs []string
		want  string
		ok    bool
	}{
		{"bare address", []string{"r-" + tok + "@reply.example.com"}, tok, true},
		{"display name form", []string{"Orders <r-" + tok + "@reply.example.com>"}, tok, true},
		{"mixed case survives lowercasing in transit", []string{"R-" + strings.ToUpper(tok) + "@Reply.Example.com"}, tok, true},
		{"picks ours out of several recipients", []string{"customer@gmail.com", "r-" + tok + "@reply.example.com"}, tok, true},
		{"wrong domain", []string{"r-" + tok + "@evil.com"}, "", false},
		{"no prefix", []string{tok + "@reply.example.com"}, "", false},
		{"short token", []string{"r-abc@reply.example.com"}, "", false},
		{"token with digits outside base32", []string{"r-" + strings.Repeat("1", 20) + "@reply.example.com"}, "", false},
		{"not an address", []string{"garbage"}, "", false},
		{"empty", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := TokenFromAddresses(tc.addrs, domain)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("got (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
