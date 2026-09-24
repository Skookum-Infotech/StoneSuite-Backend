package vendorbill

import (
	"errors"
	"strings"
	"testing"
)

func TestIsConvertibleStatus(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"PART", true}, // partly received: the first delivery can be billed
		{"RCVD", true},
		{"CLSD", true}, // short-closed, still owes for what arrived
		{"DRFT", false},
		{"PAPV", false},
		{"APPV", false},
		{"SENT", false}, // nothing has arrived yet
		{"CANC", false},
		{"", false},
		{"XXXX", false},
	}
	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			if got := IsConvertibleStatus(tt.code); got != tt.want {
				t.Fatalf("IsConvertibleStatus(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestBillableQuantity(t *testing.T) {
	tests := []struct {
		name             string
		received, billed float64
		want             float64
	}{
		{"nothing received", 0, 0, 0},
		{"first delivery, nothing billed", 6, 0, 6},
		{"second delivery bills only the new goods", 10, 6, 4},
		{"fully billed", 10, 10, 0},
		{"receipt voided after billing leaves nothing, not a negative", 4, 6, 0},
		{"fractional quantities", 2.5, 1, 1.5},
		{"float noise is rounded away", 5.1, 2.1, 3},
		{"sub-thousandth remainder is nothing", 1.0004, 1, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := billableQuantity(tt.received, tt.billed); got != tt.want {
				t.Fatalf("billableQuantity(%v, %v) = %v, want %v", tt.received, tt.billed, got, tt.want)
			}
		})
	}
}

func TestPlanConversion(t *testing.T) {
	line := func(n int, ordered, received, billed float64) poSourceLine {
		return poSourceLine{lineNumber: n, quantity: ordered, qtyReceived: received, qtyBilled: billed}
	}

	t.Run("partly received order bills only what arrived", func(t *testing.T) {
		got, err := planConversion([]poSourceLine{line(1, 10, 4, 0), line(2, 5, 0, 0), line(3, 8, 8, 0)})
		if err != nil {
			t.Fatalf("planConversion: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("billable lines = %d, want 2 (the untouched line is left off)", len(got))
		}
		if got[0].lineNumber != 1 || got[0].billQty != 4 {
			t.Errorf("line 1 = #%d qty %v, want #1 qty 4", got[0].lineNumber, got[0].billQty)
		}
		if got[1].lineNumber != 3 || got[1].billQty != 8 {
			t.Errorf("line 2 = #%d qty %v, want #3 qty 8", got[1].lineNumber, got[1].billQty)
		}
	})

	t.Run("later delivery bills only the goods not yet billed", func(t *testing.T) {
		got, err := planConversion([]poSourceLine{line(1, 10, 10, 4)})
		if err != nil {
			t.Fatalf("planConversion: %v", err)
		}
		if len(got) != 1 || got[0].billQty != 6 {
			t.Fatalf("got %+v, want one line billing 6", got)
		}
	})

	t.Run("does not mutate the caller's lines", func(t *testing.T) {
		in := []poSourceLine{line(1, 10, 4, 0)}
		if _, err := planConversion(in); err != nil {
			t.Fatalf("planConversion: %v", err)
		}
		if in[0].billQty != 0 {
			t.Errorf("input billQty = %v, want it left at 0", in[0].billQty)
		}
	})

	errCases := []struct {
		name    string
		lines   []poSourceLine
		wantMsg string
	}{
		{"no lines", nil, "no line items"},
		{"nothing received yet", []poSourceLine{line(1, 10, 0, 0), line(2, 5, 0, 0)}, "Nothing has been received"},
		{"everything received is already billed", []poSourceLine{line(1, 10, 10, 10), line(2, 5, 3, 3)}, "already been billed"},
	}
	for _, tt := range errCases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := planConversion(tt.lines)
			var ce ClientError
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want a ClientError", err)
			}
			if !strings.Contains(ce.Msg, tt.wantMsg) {
				t.Errorf("message %q does not mention %q", ce.Msg, tt.wantMsg)
			}
		})
	}
}
