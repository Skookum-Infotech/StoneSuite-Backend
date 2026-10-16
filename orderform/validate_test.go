package orderform

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// now is a fixed clock: Wednesday 14 October 2026, UTC.
var now = time.Date(2026, time.October, 14, 15, 30, 0, 0, time.UTC)

func kitchen() Area {
	return Area{Kind: "kitchen", Name: "Kitchen", Color: "Serene", FaucetSpread: []string{}}
}

func validInput() Input {
	return Input{
		Builder: "Highland Homes", Community: "Sandbrock Ranch", Plan: "214",
		Phone: "+14802095330", Email: "brandon@highlandhomes.com",
		InstallAddress: "741 Bridle Path", City: "Aubrey", Zip: "76227",
		MeasureDate: "2026-10-16", InstallDate: "2026-10-26",
		Areas: []Area{kitchen()},
	}
}

func TestNormalize_AcceptsAndTidiesAValidOrder(t *testing.T) {
	in := validInput()
	in.Builder = "  Highland   Homes "
	in.SpecialInstructions = "  Call first.\x00 "
	in.Areas[0].Color = " Serene  Mist "

	out, err := Normalize(in, now)

	require.NoError(t, err)
	assert.Equal(t, "Highland Homes", out.Builder)
	assert.Equal(t, "Call first.", out.SpecialInstructions)
	assert.Equal(t, "Serene Mist", out.Areas[0].Color)
}

func TestNormalizeEdit_AcceptsTheStoredInstallDateEvenOncePast(t *testing.T) {
	in := validInput()
	in.MeasureDate, in.InstallDate = "2026-09-20", "2026-10-02" // before 'now' (14 Oct)

	_, err := NormalizeEdit(in, now, "2026-10-02")

	require.NoError(t, err)
}

func TestNormalizeEdit_ADifferentPastInstallDateIsStillRefused(t *testing.T) {
	in := validInput()
	in.MeasureDate, in.InstallDate = "2026-09-20", "2026-10-03"

	_, err := NormalizeEdit(in, now, "2026-10-02")

	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Contains(t, ve.Message, "cannot be in the past")
}

func TestNormalize_HeaderProblems(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Input)
		want   string
	}{
		{"builder missing", func(i *Input) { i.Builder = "  " }, "Builder is required."},
		{"community missing", func(i *Input) { i.Community = "" }, "Community is required."},
		{"phone missing", func(i *Input) { i.Phone = "" }, "Phone is required."},
		{"phone too short", func(i *Input) { i.Phone = "12345" }, "valid phone"},
		{"phone with letters only", func(i *Input) { i.Phone = "call me" }, "valid phone"},
		{"email missing", func(i *Input) { i.Email = "" }, "Email is required."},
		{"email malformed", func(i *Input) { i.Email = "brandon@" }, "valid email"},
		{"address missing", func(i *Input) { i.InstallAddress = "" }, "Install Address is required."},
		{"address too long", func(i *Input) { i.InstallAddress = strings.Repeat("a", 101) }, "at most 100"},
		{"city missing", func(i *Input) { i.City = "" }, "City is required."},
		{"zip too long", func(i *Input) { i.Zip = "12345678901" }, "at most 10"},
		{"plan too long", func(i *Input) { i.Plan = strings.Repeat("p", 121) }, "Plan can be at most"},
		{"instructions too long", func(i *Input) { i.SpecialInstructions = strings.Repeat("x", 2001) }, "Special Instructions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			tc.mutate(&in)

			_, err := Normalize(in, now)

			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			assert.Contains(t, ve.Message, tc.want)
		})
	}
}

func TestNormalize_Dates(t *testing.T) {
	cases := []struct {
		name             string
		measure, install string
		want             string // "" = valid
	}{
		{"both in the future", "2026-10-16", "2026-10-26", ""},
		{"install the same day as measure", "2026-10-16", "2026-10-16", ""},
		{"install today", "2026-10-14", "2026-10-14", ""},
		{"install yesterday is within the grace", "2026-10-13", "2026-10-13", ""},
		{"install two days ago", "2026-10-10", "2026-10-12", "cannot be in the past"},
		{"install before measure", "2026-10-20", "2026-10-18", "before the Measure Date"},
		{"measure missing", "", "2026-10-26", "Measure Date is required."},
		{"install missing", "2026-10-16", "", "Install Date is required."},
		{"measure not a date", "10/16/2026", "2026-10-26", "Measure Date must be a valid date."},
		{"install not a date", "2026-10-16", "2026-02-30", "Install Date must be a valid date."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validInput()
			in.MeasureDate, in.InstallDate = tc.measure, tc.install

			_, err := Normalize(in, now)

			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			assert.Contains(t, ve.Message, tc.want)
		})
	}
}
