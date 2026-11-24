package employee

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"PEND", "ACTV", true},
		{"PEND", "INAC", true},
		{"ACTV", "INAC", true},
		{"INAC", "ACTV", true},
		{"ACTV", "PEND", false},
		{"INAC", "PEND", false},
		{"ACTV", "ACTV", false},
		{"UNKNOWN", "ACTV", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, CanTransition(tt.from, tt.to), "%s -> %s", tt.from, tt.to)
	}
	assert.ErrorIs(t, ValidateTransition("ACTV", "PEND"), ErrInvalidTransition)
	assert.NoError(t, ValidateTransition("PEND", "ACTV"))
}

func TestNextStatuses_Sorted(t *testing.T) {
	assert.Equal(t, []string{"ACTV", "INAC"}, NextStatuses("PEND"))
	assert.Equal(t, []string{"INAC"}, NextStatuses("ACTV"))
	assert.Empty(t, NextStatuses("UNKNOWN"))
}

func TestEmployeeFields_Validate(t *testing.T) {
	ok := employeeFields{FirstName: "Ana", Email: "ana@example.com"}
	tests := []struct {
		name    string
		mutate  func(f *employeeFields)
		wantMsg string
	}{
		{"valid", func(f *employeeFields) {}, ""},
		{"first name required", func(f *employeeFields) { f.FirstName = "" }, "First name is required."},
		{"email required", func(f *employeeFields) { f.Email = "" }, "Email is required."},
		{"email malformed", func(f *employeeFields) { f.Email = "not-an-email" }, "Enter a valid email address."},
		{"email with display name", func(f *employeeFields) { f.Email = "Ana <ana@example.com>" }, "Enter a valid email address."},
		{"first name too long", func(f *employeeFields) { f.FirstName = strings.Repeat("a", maxNameLen+1) }, "First name must be at most 100 characters."},
		{"phone too long", func(f *employeeFields) { f.Phone = strings.Repeat("1", maxPhoneLen+1) }, "Phone must be at most 30 characters."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := ok
			tt.mutate(&f)
			err := f.normalize().validate()
			if tt.wantMsg == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.wantMsg)
		})
	}
}

func TestEmployeeFields_NormalizeTrims(t *testing.T) {
	f := employeeFields{FirstName: "  Ana ", LastName: " Diaz", Email: " ana@example.com ", Phone: " 555 "}.normalize()
	assert.Equal(t, "Ana", f.FirstName)
	assert.Equal(t, "Diaz", f.LastName)
	assert.Equal(t, "ana@example.com", f.Email)
	assert.Equal(t, "555", f.Phone)
}

func TestDisplayName(t *testing.T) {
	assert.Equal(t, "Ana Diaz", displayName("Ana", "Diaz", "ana@example.com"))
	assert.Equal(t, "Ana", displayName("Ana", "", "ana@example.com"))
	assert.Equal(t, "ana@example.com", displayName("", "", "ana@example.com"))
}

func TestFormatNumber(t *testing.T) {
	assert.Equal(t, "EMPL-000042", FormatNumber(42))
}

// Every key in sortFields must be handled by sortValue. A missing case falls
// through to the created_at default, so the keyset cursor would be minted from
// a different column than the query ordered by.
func TestSortValueCoversEverySortField(t *testing.T) {
	sentinel := time.Date(1999, 1, 1, 0, 0, 0, 0, time.UTC)
	probe := Employee{Number: "EMPL-000001", CreatedAt: sentinel, UpdatedAt: time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)}
	probe.FirstName, probe.LastName, probe.Email = "Ana", "Diaz", "Ana@Example.com"

	for key := range sortFields {
		if key == "created_at" {
			continue // the default branch IS created_at
		}
		got := sortValue(probe, empMeta{statusID: 7}, key)
		if v, ok := got.(time.Time); ok && v.Equal(sentinel) {
			t.Errorf("sortValue(%q) fell through to created_at", key)
		}
	}
	assert.Equal(t, "ana diaz", sortValue(probe, empMeta{}, "name"))
	assert.Equal(t, "ana@example.com", sortValue(probe, empMeta{}, "email"))
	assert.Equal(t, 7, sortValue(probe, empMeta{statusID: 7}, "status"))
}

// The query builder refuses a sort key that Resolve does not know, so every
// sortFields key must also be a filter field -- a mismatch only shows up as a
// 400 on the list page.
func TestEverySortFieldIsAlsoAFilterField(t *testing.T) {
	r := resolver{}
	for key := range sortFields {
		_, _, ok := r.Resolve(key)
		assert.True(t, ok, "sort field %q is not resolvable as a filter field", key)
	}
}
