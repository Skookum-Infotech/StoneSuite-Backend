package orderform

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func withAreas(areas ...Area) Input {
	in := validInput()
	in.Areas = areas
	return in
}

func TestNormalize_AreaProblems(t *testing.T) {
	cases := []struct {
		name  string
		areas []Area
		want  string
	}{
		{"no areas", nil, "at least one area"},
		{"unknown kind", []Area{{Kind: "garage", Name: "Garage", Color: "x"}}, "Unknown area type"},
		{"single-use kind twice", []Area{kitchen(), {Kind: "kitchen", Name: "Kitchen 2", Color: "x"}}, "only be added once"},
		{"same name twice", []Area{
			{Kind: "bath", Name: "Guest Bath", Color: "x"}, {Kind: "bath", Name: "guest  bath", Color: "y"},
		}, "already named"},
		{"name missing", []Area{{Kind: "bath", Name: " ", Color: "x"}}, "Give every area a name"},
		{"name too long", []Area{{Kind: "bath", Name: strings.Repeat("b", 101), Color: "x"}}, "at most 100"},
		{"color missing", []Area{{Kind: "kitchen", Name: "Kitchen", Color: " "}}, "Color is required"},
		{"backsplash not a number", []Area{{Kind: "kitchen", Name: "Kitchen", Color: "x", BacksplashHeight: "tall"}}, "Backsplash Height"},
		{"backsplash too tall", []Area{{Kind: "kitchen", Name: "Kitchen", Color: "x", BacksplashHeight: "97"}}, "Backsplash Height"},
		{"backsplash negative", []Area{{Kind: "kitchen", Name: "Kitchen", Color: "x", BacksplashHeight: "-1"}}, "Backsplash Height"},
		{"waterfall too many", []Area{{Kind: "kitchen", Name: "Kitchen", Color: "x", WaterfallQty: 21}}, "Waterfall Qty"},
		{"pop-up negative", []Area{{Kind: "island", Name: "Island", Color: "x", PopUpOutlet: -1}}, "Pop-up Outlet"},
		{"spread position not on the row", []Area{
			{Kind: "master_bath", Name: "Primary / Master Bath", Color: "x", FaucetSpread: []string{"L12"}},
		}, "Faucet Spread"},
		{"notes too long", []Area{{Kind: "kitchen", Name: "Kitchen", Color: "x", Notes: strings.Repeat("n", 1001)}}, "notes can be at most"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Normalize(withAreas(tc.areas...), now)

			var ve *ValidationError
			require.ErrorAs(t, err, &ve)
			assert.Contains(t, ve.Message, tc.want)
		})
	}
}

func TestNormalize_TooManyAreas(t *testing.T) {
	areas := make([]Area, 0, MaxAreas+1)
	for i := 0; i <= MaxAreas; i++ {
		areas = append(areas, Area{Kind: "bath", Name: "Bath " + strings.Repeat("x", i+1), Color: "x"})
	}

	_, err := Normalize(withAreas(areas...), now)

	var ve *ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Contains(t, ve.Message, "at most 20 areas")
}

func TestNormalize_BlanksWhatTheKindDoesNotShow(t *testing.T) {
	island := Area{
		Kind: "island", Name: "Island", Color: "Serene", Finish: "Polished",
		BacksplashHeight: "4", Location: "Window sill", HalfWall: true, WaterfallQty: 1, PopUpOutlet: 2,
	}
	pantry := Area{Kind: "pantry", Name: "Pantry", Color: "Serene", CooktopModel: "X1", WaterfallQty: 3, PopUpOutlet: 1}

	out, err := Normalize(withAreas(island, pantry), now)

	require.NoError(t, err)
	assert.Equal(t, "", out.Areas[0].BacksplashHeight, "an island has no backsplash height")
	assert.Equal(t, "", out.Areas[0].Location)
	assert.False(t, out.Areas[0].HalfWall)
	assert.Equal(t, 1, out.Areas[0].WaterfallQty)
	assert.Equal(t, 2, out.Areas[0].PopUpOutlet)
	assert.Equal(t, "Polished", out.Areas[0].Finish)
	assert.Equal(t, "", out.Areas[1].CooktopModel)
	assert.Equal(t, 0, out.Areas[1].WaterfallQty)
	assert.Equal(t, 0, out.Areas[1].PopUpOutlet)
}

func TestNormalize_BacksplashHeight(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""}, {"0", ""}, {"4", "4"}, {"4.25", "4.25"}, {" 6 ", "6"}, {"4.256", "4.26"}, {"96", "96"},
	}
	for _, tc := range cases {
		t.Run("height "+tc.in, func(t *testing.T) {
			a := kitchen()
			a.BacksplashHeight = tc.in

			out, err := Normalize(withAreas(a), now)

			require.NoError(t, err)
			assert.Equal(t, tc.want, out.Areas[0].BacksplashHeight)
		})
	}
}

func TestNormalize_FaucetSpreadIsOrderedAndDeduplicated(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		given []string
		want  []string
	}{
		{"wide row, left to right", "kitchen", []string{"R12", "C", "L8", "C"}, []string{"L8", "C", "R12"}},
		{"narrow row, left to right", "bath", []string{"R2", "L2"}, []string{"L2", "R2"}},
		{"none", "kitchen", nil, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Area{Kind: tc.kind, Name: "Area", Color: "x", FaucetSpread: tc.given}

			out, err := Normalize(withAreas(a), now)

			require.NoError(t, err)
			assert.Equal(t, tc.want, out.Areas[0].FaucetSpread)
		})
	}
}
