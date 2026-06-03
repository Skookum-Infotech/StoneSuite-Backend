package workflow

import (
	"errors"
	"testing"
)

func f(key string, dt DataType, required bool, opts ...string) FieldDefinition {
	return FieldDefinition{Key: key, Label: key, DataType: dt, Required: required, Options: opts}
}

func TestValidateCustomFields(t *testing.T) {
	min := 1.0
	max := 100.0
	defs := []FieldDefinition{
		f("source", TypeEnum, false, "web", "referral"),
		f("phone", TypeString, false),
		f("email", TypeEmail, true),
		f("count", TypeNumber, false),
		f("active", TypeBool, false),
		f("when", TypeDate, false),
		{Key: "score", Label: "Score", DataType: TypeNumber, Validation: FieldValidation{Min: &min, Max: &max}},
	}

	tests := []struct {
		name    string
		values  map[string]any
		wantErr bool
	}{
		{"all valid", map[string]any{"email": "a@b.com", "source": "web", "count": 3.0, "active": true, "when": "2026-01-02"}, false},
		{"missing required email", map[string]any{"source": "web"}, true},
		{"empty required email", map[string]any{"email": "  "}, true},
		{"unknown key rejected", map[string]any{"email": "a@b.com", "ghost": "x"}, true},
		{"bad email", map[string]any{"email": "not-an-email"}, true},
		{"enum out of range", map[string]any{"email": "a@b.com", "source": "cold"}, true},
		{"number wrong type", map[string]any{"email": "a@b.com", "count": "three"}, true},
		{"bool wrong type", map[string]any{"email": "a@b.com", "active": "yes"}, true},
		{"date bad format", map[string]any{"email": "a@b.com", "when": "01/02/2026"}, true},
		{"date rfc3339 ok", map[string]any{"email": "a@b.com", "when": "2026-01-02T15:04:05Z"}, false},
		{"number below min", map[string]any{"email": "a@b.com", "score": 0.0}, true},
		{"number above max", map[string]any{"email": "a@b.com", "score": 200.0}, true},
		{"number within range", map[string]any{"email": "a@b.com", "score": 50.0}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCustomFields(defs, tc.values)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateFieldDefinition(t *testing.T) {
	tests := []struct {
		name    string
		def     FieldDefinition
		wantErr bool
	}{
		{"valid string", f("phone", TypeString, false), false},
		{"empty key", f("", TypeString, false), true},
		{"bad key chars", f("Phone Number", TypeString, false), true},
		{"unknown type", f("x", DataType("blob"), false), true},
		{"enum without options", f("src", TypeEnum, false), true},
		{"enum with options", f("src", TypeEnum, false, "a", "b"), false},
		{"bad regex", FieldDefinition{Key: "x", DataType: TypeString, Validation: FieldValidation{Regex: "([a-z"}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateFieldDefinition(tc.def)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidationErrorsAsType(t *testing.T) {
	err := ValidateCustomFields([]FieldDefinition{f("email", TypeEmail, true)}, map[string]any{})
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected ValidationErrors, got %T", err)
	}
	if len(ve) == 0 {
		t.Fatal("expected at least one field error")
	}
}
