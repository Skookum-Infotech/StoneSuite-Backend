package bintransfer

import (
	"strings"
	"testing"
)

func okLines(n int) []LineInput {
	out := make([]LineInput, n)
	for i := range out {
		out[i] = LineInput{
			UnitID:  "11111111-1111-1111-1111-" + strings.Repeat("0", 11) + string(rune('a'+i)),
			ToBinID: "22222222-2222-2222-2222-222222222222",
		}
	}
	return out
}

func TestValidateInput(t *testing.T) {
	tests := []struct {
		name    string
		in      Input
		wantMsg string // substring of the ClientError; "" means valid
	}{
		{"valid single line", Input{WarehouseID: 1, Lines: okLines(1)}, ""},
		{"valid many lines", Input{WarehouseID: 1, Lines: okLines(5)}, ""},
		{"missing location", Input{Lines: okLines(1)}, "location"},
		{"no lines", Input{WarehouseID: 1}, "at least one slab"},
		{"blank unit", Input{WarehouseID: 1, Lines: []LineInput{{UnitID: "  ", ToBinID: "b"}}}, "Line 1 needs a slab"},
		{"blank destination", Input{WarehouseID: 1, Lines: []LineInput{{UnitID: "u", ToBinID: ""}}}, "Line 1 needs a destination bin"},
		{"second line blank destination", Input{WarehouseID: 1, Lines: []LineInput{
			{UnitID: "u1", ToBinID: "b"}, {UnitID: "u2"},
		}}, "Line 2 needs a destination bin"},
		{"same slab twice", Input{WarehouseID: 1, Lines: []LineInput{
			{UnitID: "u1", ToBinID: "b"}, {UnitID: "u1", ToBinID: "b2"},
		}}, "Line 2 repeats"},
		{"too many lines", Input{WarehouseID: 1, Lines: okLines(MaxLines + 1)}, "at most"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateInput(&tc.in)
			if tc.wantMsg == "" {
				if err != nil {
					t.Fatalf("validateInput() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateInput() = nil, want an error containing %q", tc.wantMsg)
			}
			if !IsClientError(err) {
				t.Fatalf("validateInput() = %T, want ClientError (a 400, not a 500)", err)
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("validateInput() = %q, want it to contain %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

func TestValidateInput_TrimsIdentifiers(t *testing.T) {
	in := Input{WarehouseID: 1, Lines: []LineInput{{UnitID: " u1 ", ToBinID: " b1 "}}}
	if err := validateInput(&in); err != nil {
		t.Fatalf("validateInput() = %v", err)
	}
	if in.Lines[0].UnitID != "u1" || in.Lines[0].ToBinID != "b1" {
		t.Errorf("identifiers not trimmed: %+v", in.Lines[0])
	}
}

// lineOutcome is what completing one line does with a slab that is now in
// curBinID. The destination is the thing that was approved, so a slab that has
// since wandered to a third bin is still sent to it; one already there is left.
func TestLineOutcome(t *testing.T) {
	five, nine := 5, 9
	tests := []struct {
		name   string
		curBin *int
		toBin  int
		want   outcome
	}{
		{"moves from the planned bin", &five, 9, outcomeMove},
		{"moves from no bin at all", nil, 9, outcomeMove},
		{"already in the destination", &nine, 9, outcomeAlreadyThere},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lineOutcome(tc.curBin, tc.toBin); got != tc.want {
				t.Errorf("lineOutcome() = %v, want %v", got, tc.want)
			}
		})
	}
}
