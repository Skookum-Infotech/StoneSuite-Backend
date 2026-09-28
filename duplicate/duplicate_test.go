package duplicate

import (
	"fmt"
	"strings"
	"testing"
)

func TestKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowercases", "Acme Stone", "acme stone"},
		{"trims", "  Acme Stone  ", "acme stone"},
		{"collapses inner whitespace", "Acme   Stone", "acme stone"},
		{"tabs and newlines count as whitespace", "Acme\t\nStone", "acme stone"},
		{"same name in different case is equal", "ACME STONE", "acme stone"},
		{"blank stays blank", "   ", ""},
		{"punctuation is significant", "Acme Stone, Inc.", "acme stone, inc."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Key(tt.in); got != tt.want {
				t.Fatalf("Key(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestErrorMessage(t *testing.T) {
	tests := []struct {
		name string
		err  Error
		want string
	}{
		{
			"names the existing record and its status",
			Error{Entity: EntityCustomer, Name: "acme", ExistingName: "Acme", ExistingStatus: "Inactive"},
			`A customer named "Acme" already exists (Inactive). Use the existing record, or choose a different name.`,
		},
		{
			"falls back to the attempted name",
			Error{Entity: EntityVendor, Name: "Nero Marble"},
			`A vendor named "Nero Marble" already exists. Use the existing record, or choose a different name.`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Fatalf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAs(t *testing.T) {
	dup := &Error{Entity: EntityItem, Name: "Slab", ExistingID: "id-1"}

	if got, ok := As(fmt.Errorf("create item: %w", dup)); !ok || got != dup {
		t.Fatalf("As should unwrap a wrapped *Error, got (%v, %v)", got, ok)
	}
	if _, ok := As(fmt.Errorf("something else")); ok {
		t.Fatal("As must not match an unrelated error")
	}
	if _, ok := As(nil); ok {
		t.Fatal("As(nil) must not match")
	}
}

func TestNormalizeSQL(t *testing.T) {
	got := NormalizeSQL("c.customer_name")
	for _, want := range []string{"lower(", "btrim(", "regexp_replace(c.customer_name", `'\s+'`} {
		if !strings.Contains(got, want) {
			t.Errorf("NormalizeSQL = %q, want it to contain %q", got, want)
		}
	}
}
