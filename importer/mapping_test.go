package importer

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/workflow"
)

func TestApplyColumnMapping(t *testing.T) {
	numberDefs := []workflow.FieldDefinition{{Key: "budget", DataType: workflow.TypeNumber}}

	tests := []struct {
		name    string
		row     map[string]string
		mapping map[string]string
		defs    []workflow.FieldDefinition
		want    MappedFields
	}{
		{
			name:    "core and custom fields split, custom value coerced to its DataType",
			row:     map[string]string{"Name": "Acme", "Budget": "5000", "Notes": "ignore me"},
			mapping: map[string]string{"Name": "core:name", "Budget": "cf:budget", "Notes": ""},
			defs:    numberDefs,
			want: MappedFields{
				Core:   map[string]any{"name": "Acme"},
				Custom: map[string]any{"budget": 5000.0},
			},
		},
		{
			name:    "column absent from mapping is skipped",
			row:     map[string]string{"Extra": "value"},
			mapping: map[string]string{},
			want:    MappedFields{Core: map[string]any{}, Custom: map[string]any{}},
		},
		{
			name:    "unrecognized prefix is skipped",
			row:     map[string]string{"Weird": "value"},
			mapping: map[string]string{"Weird": "meta:key"},
			want:    MappedFields{Core: map[string]any{}, Custom: map[string]any{}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ApplyColumnMapping(tc.row, tc.mapping, tc.defs)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestCoerceCustomValue(t *testing.T) {
	tests := []struct {
		name string
		def  workflow.FieldDefinition
		raw  string
		want any
	}{
		{"number parses", workflow.FieldDefinition{DataType: workflow.TypeNumber}, "5000", 5000.0},
		{"number with decimal parses", workflow.FieldDefinition{DataType: workflow.TypeNumber}, "12.5", 12.5},
		{"unparseable number falls back to raw string", workflow.FieldDefinition{DataType: workflow.TypeNumber}, "not-a-number", "not-a-number"},
		{"bool true parses", workflow.FieldDefinition{DataType: workflow.TypeBool}, "true", true},
		{"bool false parses", workflow.FieldDefinition{DataType: workflow.TypeBool}, "false", false},
		{"unparseable bool falls back to raw string", workflow.FieldDefinition{DataType: workflow.TypeBool}, "maybe", "maybe"},
		{"string stays a string", workflow.FieldDefinition{DataType: workflow.TypeString}, "hello", "hello"},
		{"email stays a string", workflow.FieldDefinition{DataType: workflow.TypeEmail}, "a@b.com", "a@b.com"},
		{"enum stays a string", workflow.FieldDefinition{DataType: workflow.TypeEnum}, "high", "high"},
		{"date stays a string", workflow.FieldDefinition{DataType: workflow.TypeDate}, "2026-01-01", "2026-01-01"},
		{"empty number cell stays an empty string, not 0", workflow.FieldDefinition{DataType: workflow.TypeNumber}, "", ""},
		{"whitespace-only cell stays as-is", workflow.FieldDefinition{DataType: workflow.TypeBool}, "  ", "  "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, coerceCustomValue(tc.def, tc.raw))
		})
	}
}

func TestValidateMapping(t *testing.T) {
	defs := []workflow.FieldDefinition{{Key: "budget"}, {Key: "priority"}}

	tests := []struct {
		name    string
		mapping map[string]string
		wantErr bool
	}{
		{
			name:    "empty target is fine",
			mapping: map[string]string{"col": ""},
			wantErr: false,
		},
		{
			name:    "core prefix always accepted",
			mapping: map[string]string{"col": "core:anything"},
			wantErr: false,
		},
		{
			name:    "known custom field accepted",
			mapping: map[string]string{"col": "cf:budget"},
			wantErr: false,
		},
		{
			name:    "unknown custom field rejected",
			mapping: map[string]string{"col": "cf:not_a_field"},
			wantErr: true,
		},
		{
			name:    "invalid prefix rejected",
			mapping: map[string]string{"col": "meta:key"},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMapping(tc.mapping, defs)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidationMessages(t *testing.T) {
	t.Run("nil error yields nil", func(t *testing.T) {
		assert.Nil(t, validationMessages(nil))
	})

	t.Run("ValidationErrors flattens to field: message", func(t *testing.T) {
		err := workflow.ValidationErrors{
			{Field: "budget", Message: "is required"},
			{Field: "priority", Message: "must be one of low, high"},
		}
		got := validationMessages(err)
		assert.Equal(t, []string{"budget: is required", "priority: must be one of low, high"}, got)
	})

	t.Run("plain error falls back to Error() text", func(t *testing.T) {
		got := validationMessages(errors.New("boom"))
		assert.Equal(t, []string{"boom"}, got)
	})
}
