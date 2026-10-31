package linedim

import (
	"math"
	"strings"
	"testing"
)

func f(v float64) *float64 { return &v }
func n(v int) *int         { return &v }

func TestResolve(t *testing.T) {
	tests := []struct {
		name      string
		in        Input
		area      string
		wantErr   string // substring; "" = no error
		wantQty   float64
		wantDeriv bool
		wantSlabs *int
		wantUnit  string
	}{
		{name: "nothing sent leaves the line alone", in: Input{}, area: "SQFT"},
		{name: "nothing sent is fine on a non-area unit", in: Input{}, area: "EA"},
		{name: "a unit alone is not dimensions", in: Input{Unit: "in"}, area: "EA"},

		// 304.8 x 152.4 cm is exactly 10 ft x 5 ft = 50 sqft.
		{name: "one slab in cm", in: Input{Length: f(304.8), Width: f(152.4), Slabs: n(1), Unit: "cm"}, area: "SQFT",
			wantQty: 50, wantDeriv: true, wantSlabs: n(1), wantUnit: "cm"},
		{name: "blank unit means inches", in: Input{Length: f(120), Width: f(60)}, area: "SQFT",
			wantQty: 50, wantDeriv: true, wantUnit: "in"},
		{name: "three slabs", in: Input{Length: f(304.8), Width: f(152.4), Slabs: n(3), Unit: "cm"}, area: "SQFT",
			wantQty: 150, wantDeriv: true, wantSlabs: n(3), wantUnit: "cm"},
		{name: "blank slabs counts as one and stays blank", in: Input{Length: f(304.8), Width: f(152.4), Unit: "cm"}, area: "SQFT",
			wantQty: 50, wantDeriv: true, wantUnit: "cm"},

		// Same 50 sq ft slab, three other ways.
		{name: "inches", in: Input{Length: f(120), Width: f(60), Slabs: n(1), Unit: "in"}, area: "SQFT",
			wantQty: 50, wantDeriv: true, wantSlabs: n(1), wantUnit: "in"},
		{name: "feet", in: Input{Length: f(10), Width: f(5), Slabs: n(1), Unit: "ft"}, area: "SQFT",
			wantQty: 50, wantDeriv: true, wantSlabs: n(1), wantUnit: "ft"},
		{name: "millimetres", in: Input{Length: f(3048), Width: f(1524), Unit: "mm"}, area: "SQFT",
			wantQty: 50, wantDeriv: true, wantUnit: "mm"},
		// Dimensions are kept to two decimals, so metres resolve to a centimetre.
		{name: "metres into square feet", in: Input{Length: f(3), Width: f(1.5), Unit: "m"}, area: "SQFT",
			wantQty: 48.438, wantDeriv: true, wantUnit: "m"},
		{name: "two inch slabs", in: Input{Length: f(120), Width: f(60), Slabs: n(2), Unit: "in"}, area: "SQFT",
			wantQty: 100, wantDeriv: true, wantSlabs: n(2), wantUnit: "in"},
		{name: "square metres from centimetres", in: Input{Length: f(300), Width: f(150), Slabs: n(2), Unit: "cm"}, area: "SQM",
			wantQty: 9, wantDeriv: true, wantSlabs: n(2), wantUnit: "cm"},
		{name: "square metres from metres", in: Input{Length: f(3), Width: f(1.5), Unit: "m"}, area: "SQM",
			wantQty: 4.5, wantDeriv: true, wantUnit: "m"},
		{name: "unit and area code are case and space tolerant", in: Input{Length: f(120), Width: f(60), Unit: " IN "}, area: " sqft ",
			wantQty: 50, wantDeriv: true, wantUnit: "in"},
		{name: "rounds the area to three decimals", in: Input{Length: f(300), Width: f(150), Slabs: n(1), Unit: "cm"}, area: "SQFT",
			wantQty: 48.438, wantDeriv: true, wantSlabs: n(1), wantUnit: "cm"},

		{name: "slabs alone is carried, nothing derived", in: Input{Slabs: n(4), Unit: "in"}, area: "SQFT", wantSlabs: n(4), wantUnit: "in"},

		{name: "unknown unit", in: Input{Length: f(10), Width: f(10), Unit: "yd"}, area: "SQFT", wantErr: "must be one of mm, cm, in, ft, m"},
		{name: "length without width", in: Input{Length: f(100)}, area: "SQFT", wantErr: "both length and width"},
		{name: "width without length", in: Input{Width: f(100), Slabs: n(2)}, area: "SQFT", wantErr: "both length and width"},
		{name: "zero length", in: Input{Length: f(0), Width: f(10)}, area: "SQFT", wantErr: "greater than zero"},
		{name: "negative width", in: Input{Length: f(10), Width: f(-1)}, area: "SQFT", wantErr: "greater than zero"},
		{name: "100 m is the cap in metres", in: Input{Length: f(100.01), Width: f(1), Unit: "m"}, area: "SQFT", wantErr: "100 m"},
		{name: "the cap holds in any unit", in: Input{Length: f(4000), Width: f(10), Unit: "in"}, area: "SQFT", wantErr: "100 m"},
		{name: "exactly the cap passes", in: Input{Length: f(10000), Width: f(1), Unit: "cm"}, area: "SQFT",
			wantQty: 10.764, wantDeriv: true, wantUnit: "cm"},
		{name: "zero slabs", in: Input{Length: f(10), Width: f(10), Slabs: n(0)}, area: "SQFT", wantErr: "from 1 to"},
		{name: "slabs over the cap", in: Input{Slabs: n(MaxSlabs + 1)}, area: "SQFT", wantErr: "from 1 to"},
		{name: "area too small to price", in: Input{Length: f(0.01), Width: f(0.01)}, area: "SQFT", wantErr: "too small"},

		{name: "each unit refuses dimensions", in: Input{Length: f(10), Width: f(10)}, area: "EA", wantErr: "SQFT or SQM"},
		{name: "free-text line refuses dimensions", in: Input{Length: f(10), Width: f(10)}, area: "", wantErr: "SQFT or SQM"},
		{name: "slabs alone refused on a non-area unit", in: Input{Slabs: n(2)}, area: "EA", wantErr: "SQFT or SQM"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, msg := Resolve(tt.in, tt.area)
			if tt.wantErr != "" {
				if !strings.Contains(msg, tt.wantErr) {
					t.Fatalf("message = %q, want it to contain %q", msg, tt.wantErr)
				}
				return
			}
			if msg != "" {
				t.Fatalf("unexpected message %q", msg)
			}
			if got.Derived != tt.wantDeriv {
				t.Fatalf("Derived = %v, want %v", got.Derived, tt.wantDeriv)
			}
			if math.Abs(got.Quantity-tt.wantQty) > 1e-9 {
				t.Fatalf("Quantity = %v, want %v", got.Quantity, tt.wantQty)
			}
			if got.Dims.Unit != tt.wantUnit {
				t.Fatalf("Unit = %q, want %q", got.Dims.Unit, tt.wantUnit)
			}
			switch {
			case tt.wantSlabs == nil && got.Dims.Slabs != nil:
				t.Fatalf("Slabs = %d, want nil", *got.Dims.Slabs)
			case tt.wantSlabs != nil && (got.Dims.Slabs == nil || *got.Dims.Slabs != *tt.wantSlabs):
				t.Fatalf("Slabs = %v, want %d", got.Dims.Slabs, *tt.wantSlabs)
			}
		})
	}
}

// Dimensions are stored to two decimals, so the area must be derived from the
// rounded value — otherwise the stored length and the stored quantity disagree.
func TestResolveRoundsDimensionsBeforeDeriving(t *testing.T) {
	got, msg := Resolve(Input{Length: f(30.484), Width: f(30.48), Unit: "cm"}, "SQFT")
	if msg != "" {
		t.Fatalf("unexpected message %q", msg)
	}
	if *got.Dims.Length != 30.48 {
		t.Fatalf("Length = %v, want 30.48", *got.Dims.Length)
	}
	if got.Quantity != 1 { // 30.48 x 30.48 cm is exactly one square foot
		t.Fatalf("Quantity = %v, want 1", got.Quantity)
	}
}

// 12 x 12 inches is exactly one square foot, with no float noise.
func TestResolveOneSquareFootInInches(t *testing.T) {
	got, msg := Resolve(Input{Length: f(12), Width: f(12), Unit: "in"}, "SQFT")
	if msg != "" || got.Quantity != 1 {
		t.Fatalf("got qty %v msg %q, want exactly 1", got.Quantity, msg)
	}
}

func TestNormalizeUnit(t *testing.T) {
	for raw, want := range map[string]string{"": "in", " CM ": "cm", "Ft": "ft", "mm": "mm", "m": "m"} {
		got, ok := NormalizeUnit(raw)
		if !ok || got != want {
			t.Errorf("NormalizeUnit(%q) = (%q, %v), want (%q, true)", raw, got, ok, want)
		}
	}
	for _, bad := range []string{"yd", "cm2", "inch"} {
		if _, ok := NormalizeUnit(bad); ok {
			t.Errorf("NormalizeUnit(%q) accepted", bad)
		}
	}
}

func TestStoredUnit(t *testing.T) {
	if got := (Dims{}).StoredUnit(); got != "in" {
		t.Errorf("empty Dims.StoredUnit() = %q, want in", got)
	}
	if got := (Dims{Unit: "cm"}).StoredUnit(); got != "cm" {
		t.Errorf("Dims{cm}.StoredUnit() = %q, want cm", got)
	}
}

func TestIsAreaUnit(t *testing.T) {
	for unit, want := range map[string]bool{"SQFT": true, "sqm": true, "EA": false, "": false, "LFT": false} {
		if got := IsAreaUnit(unit); got != want {
			t.Errorf("IsAreaUnit(%q) = %v, want %v", unit, got, want)
		}
	}
}

func TestResolveThickness(t *testing.T) {
	tests := []struct {
		name     string
		value    float64
		unit     string
		area     string
		wantVal  float64
		wantUnit string
		wantErr  string // substring; "" = no error
	}{
		{name: "inches", value: 0.75, unit: "in", area: "SQFT", wantVal: 0.75, wantUnit: "in"},
		{name: "blank unit means inches", value: 1.25, unit: "", area: "SQFT", wantVal: 1.25, wantUnit: "in"},
		{name: "centimetres on a square-metre line", value: 3, unit: "cm", area: "SQM", wantVal: 3, wantUnit: "cm"},
		{name: "rounds to two decimals", value: 0.754, unit: "in", area: "SQFT", wantVal: 0.75, wantUnit: "in"},
		{name: "unit and area are case tolerant", value: 2, unit: " CM ", area: " sqft ", wantVal: 2, wantUnit: "cm"},
		{name: "exactly one metre is allowed", value: 100, unit: "cm", area: "SQFT", wantVal: 100, wantUnit: "cm"},

		{name: "zero", value: 0, unit: "in", area: "SQFT", wantErr: "greater than zero"},
		{name: "negative", value: -1, unit: "in", area: "SQFT", wantErr: "greater than zero"},
		{name: "rounds down to zero", value: 0.004, unit: "in", area: "SQFT", wantErr: "greater than zero"},
		{name: "past a metre", value: 101, unit: "cm", area: "SQFT", wantErr: "cannot exceed 1 m"},
		{name: "past a metre in feet", value: 4, unit: "ft", area: "SQFT", wantErr: "cannot exceed 1 m"},
		{name: "unknown unit", value: 1, unit: "yd", area: "SQFT", wantErr: "must be one of mm, cm, in, ft, m"},
		{name: "not an area line", value: 1, unit: "in", area: "EA", wantErr: "SQFT or SQM"},
		{name: "free-text line", value: 1, unit: "in", area: "", wantErr: "SQFT or SQM"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			val, unit, msg := ResolveThickness(tc.value, tc.unit, tc.area)
			if tc.wantErr != "" {
				if !strings.Contains(msg, tc.wantErr) {
					t.Fatalf("msg = %q, want it to contain %q", msg, tc.wantErr)
				}
				return
			}
			if msg != "" {
				t.Fatalf("unexpected error %q", msg)
			}
			if math.Abs(val-tc.wantVal) > 1e-9 || unit != tc.wantUnit {
				t.Errorf("got (%v, %q), want (%v, %q)", val, unit, tc.wantVal, tc.wantUnit)
			}
		})
	}
}

func TestToMM(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		unit  string
		want  float64
	}{
		{name: "an inch is 25.4 mm", value: 1, unit: "in", want: 25.4},
		{name: "ten feet", value: 10, unit: "ft", want: 3048},
		{name: "a 120 x 60 in slab side", value: 120, unit: "in", want: 3048},
		{name: "centimetres", value: 30.48, unit: "cm", want: 304.8},
		{name: "millimetres pass through", value: 18.5, unit: "mm", want: 18.5},
		{name: "metres", value: 1.5, unit: "m", want: 1500},
		{name: "blank unit means inches", value: 2, unit: "", want: 50.8},
		{name: "rounds to two decimals", value: 0.333, unit: "in", want: 8.46},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ToMM(tc.value, tc.unit)
			if !ok || math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("ToMM(%v, %q) = (%v, %v), want (%v, true)", tc.value, tc.unit, got, ok, tc.want)
			}
		})
	}
	if _, ok := ToMM(1, "yd"); ok {
		t.Error("ToMM accepted an unknown unit")
	}
}
