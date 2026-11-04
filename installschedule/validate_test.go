package installschedule

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCrew(t *testing.T) {
	many := make([]CrewInput, 0, MaxCrew+1)
	for i := 1; i <= MaxCrew+1; i++ {
		many = append(many, CrewInput{EmployeeID: i, Role: CrewMemberRole})
	}
	tests := []struct {
		name    string
		crew    []CrewInput
		wantErr bool
	}{
		{name: "empty", crew: nil},
		{name: "members only", crew: []CrewInput{{1, CrewMemberRole}, {2, CrewMemberRole}}},
		{name: "lead and member", crew: []CrewInput{{1, CrewLead}, {2, CrewMemberRole}}},
		{name: "two leads", crew: []CrewInput{{1, CrewLead}, {2, CrewLead}}, wantErr: true},
		{name: "duplicate", crew: []CrewInput{{1, CrewMemberRole}, {1, CrewMemberRole}}, wantErr: true},
		{name: "too many", crew: many, wantErr: true},
		{name: "zero id", crew: []CrewInput{{0, CrewMemberRole}}, wantErr: true},
		{name: "unknown role", crew: []CrewInput{{1, CrewRole("boss")}}, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCrew(tc.crew)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			var ve *ValidationError
			require.True(t, errors.As(err, &ve))
			assert.Equal(t, "crew", ve.Field)
		})
	}
}

func TestValidateCreate(t *testing.T) {
	today := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	ok := CreateInput{
		FabricationJobUUID: "11111111-1111-1111-1111-111111111111",
		VisitType:          VisitInstall,
		StartLocal:         "2026-11-10T08:00",
		EndLocal:           "2026-11-10T12:00",
	}
	with := func(f func(*CreateInput)) CreateInput { in := ok; f(&in); return in }
	tests := []struct {
		name      string
		in        CreateInput
		wantField string
	}{
		{name: "valid", in: ok},
		{name: "yesterday allowed", in: with(func(c *CreateInput) { c.StartLocal = "2026-11-09T08:00" })},
		{name: "backdated", in: with(func(c *CreateInput) { c.StartLocal = "2026-11-08T08:00" }), wantField: "startLocal"},
		{name: "notes too long", in: with(func(c *CreateInput) { c.Notes = strings.Repeat("x", MaxNotes+1) }), wantField: "notes"},
		{name: "2000 multibyte runes ok", in: with(func(c *CreateInput) { c.Notes = strings.Repeat("é", MaxNotes) })},
		{name: "2001 multibyte runes rejected", in: with(func(c *CreateInput) { c.Notes = strings.Repeat("é", MaxNotes+1) }), wantField: "notes"},
		{name: "bad visit type", in: with(func(c *CreateInput) { c.VisitType = "party" }), wantField: "visitType"},
		{name: "missing job", in: with(func(c *CreateInput) { c.FabricationJobUUID = "" }), wantField: "fabricationJobId"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCreate(tc.in, today)
			if tc.wantField == "" {
				assert.NoError(t, err)
				return
			}
			var ve *ValidationError
			require.True(t, errors.As(err, &ve))
			assert.Equal(t, tc.wantField, ve.Field)
		})
	}
}

func TestValidateReason(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "empty", in: ""},
		{name: "500 ascii", in: strings.Repeat("x", MaxReason)},
		{name: "500 multibyte", in: strings.Repeat("é", MaxReason)},
		{name: "501 ascii", in: strings.Repeat("x", MaxReason+1), wantErr: true},
		{name: "501 multibyte", in: strings.Repeat("é", MaxReason+1), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateReason(tc.in)
			if !tc.wantErr {
				assert.NoError(t, err)
				return
			}
			var ve *ValidationError
			require.True(t, errors.As(err, &ve))
			assert.Equal(t, "reason", ve.Field)
		})
	}
}
