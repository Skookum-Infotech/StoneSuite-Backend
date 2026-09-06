package importer

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/workflow"
)

func TestApplyColumnMapping(t *testing.T) {
	tests := []struct {
		name    string
		row     map[string]string
		mapping map[string]string
		want    MappedFields
	}{
		{
			name:    "core and custom fields split",
			row:     map[string]string{"Name": "Acme", "Budget": "5000", "Notes": "ignore me"},
			mapping: map[string]string{"Name": "core:name", "Budget": "cf:budget", "Notes": ""},
			want: MappedFields{
				Core:   map[string]any{"name": "Acme"},
				Custom: map[string]any{"budget": "5000"},
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
			got := ApplyColumnMapping(tc.row, tc.mapping)
			assert.Equal(t, tc.want, got)
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
