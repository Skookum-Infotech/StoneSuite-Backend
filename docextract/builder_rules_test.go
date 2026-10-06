package docextract

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Rules added for homebuilder purchase orders (letterhead buyer, boxed PO
// title, label strips, Extension column, contact boilerplate).

func TestDetectTableHeader_NeedsQtyOrAmount(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		want bool
	}{
		{"label strip is not a line table", trowSplit(542, tcell{86, "Date"}, tcell{153, "Job Number"}, tcell{254, "Plan"}, tcell{326, "Cost Code"}, tcell{425, "Cost Code Description"}), false},
		{"standard header", trowSplit(560, tcell{50, "Item"}, tcell{110, "Description"}, tcell{330, "Qty"}, tcell{420, "Unit Price"}, tcell{510, "Amount"}), true},
		{"builder header", trowSplit(489, tcell{57, "Quantity"}, tcell{105, "Item #"}, tcell{185, "Description"}, tcell{331, "Extension"}, tcell{402, "Item Cost"}, tcell{455, "U/M"}, tcell{512, "Ext. Cost"}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := detectTableHeader(tt.row)
			assert.Equal(t, tt.want, ok)
		})
	}
}

func TestDetectTableHeader_RightmostAmountWins(t *testing.T) {
	cols, ok := detectTableHeader(trowSplit(489, tcell{57, "Quantity"}, tcell{185, "Description"}, tcell{331, "Extension"}, tcell{402, "Item Cost"}, tcell{512, "Ext. Cost"}))
	assert.True(t, ok)
	var roles []colRole
	for _, c := range cols {
		roles = append(roles, c.role)
	}
	assert.Equal(t, []colRole{roleQty, roleDesc, roleSkip, rolePrice, roleAmount}, roles)
}

func TestNumericCell(t *testing.T) {
	w := func(ss ...string) []Word {
		var out []Word
		for _, s := range ss {
			out = append(out, Word{Text: s})
		}
		return out
	}
	tests := []struct {
		name string
		ws   []Word
		want bool
	}{
		{"plain number", w("12.00"), true},
		{"currency sign then number", w("$", "12.00"), true},
		{"qty with unit", w("12", "EA"), true},
		{"garbled number still counts", w("1,2O0.00"), true},
		{"prose word", w("Original"), false},
		{"prose before a number", w("form", "01-339"), false},
		{"long prose run", w("01-919", "has", "been"), false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, numericCell(tt.ws))
		})
	}
}

func TestStandaloneLabel(t *testing.T) {
	tests := []struct {
		before, rest string
		want         bool
	}{
		{"", " Acme Stone", true},
		{"PO 12   ", ": Acme Stone", true},
		{"Unique Special, Change Order ", " Options", false},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, standaloneLabel(tt.before, tt.rest), "%q|%q", tt.before, tt.rest)
	}
}

func TestLossy(t *testing.T) {
	tests := []struct {
		frac string
		keep int
		want bool
	}{
		{"5100", centsDigits, false},
		{"0000", centsDigits, false},
		{"345", centsDigits, true},
		{"5", centsDigits, false},
		{"0005", milliDigits, true},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, lossy(tt.frac, tt.keep), "%s/%d", tt.frac, tt.keep)
	}
}

func TestExtractLetterhead(t *testing.T) {
	tests := []struct {
		name     string
		rows     []Row
		own      []string
		wantName string
		wantBill string
	}{
		{
			name: "issuer with address, PO number boxed to the right",
			rows: []Row{
				trowSplit(718, tcell{54, "Lakeview Homes"}, tcell{468, "Purchase Order"}),
				trowSplit(706.5, tcell{54, "4410 Commerce Parkway"}),
				trowSplit(698.2, tcell{478, "00412-K5521"}),
				trowSplit(695.7, tcell{54, "Frisco, TX 75034"}),
				trowSplit(643, tcell{54, "Vendor Address"}),
			},
			wantName: "Lakeview Homes",
			wantBill: "Lakeview Homes\n4410 Commerce Parkway\nFrisco, TX 75034",
		},
		{
			name: "a title is not an issuer",
			rows: []Row{trowSplit(750, tcell{50, "PURCHASE ORDER"}), trowSplit(735, tcell{50, "12 Main St"})},
		},
		{
			name: "a name without an address is not an issuer",
			rows: []Row{trowSplit(750, tcell{50, "Weekly Timesheet Report"}), trowSplit(735, tcell{50, "Employee Name"})},
		},
		{
			name: "our own company is never the customer",
			rows: []Row{trowSplit(750, tcell{50, "Elevation Stone, LLC"}), trowSplit(735, tcell{50, "2700 Story Rd W"})},
			own:  []string{"Elevation Stone"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rows []hrow
			for i, r := range tt.rows {
				rows = append(rows, newHRow(1, i, r))
			}
			name, bill := extractLetterhead(rows, findHits(rows), tt.own)
			assert.Equal(t, tt.wantName, name.Value)
			assert.Equal(t, tt.wantBill, bill.Value)
			if tt.wantName != "" {
				assert.Equal(t, ConfCheck, name.Confidence, "an inferred customer always needs review")
			}
		})
	}
}

func TestDetectRevision_ChangeOrderNeedsNumber(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{"Unique Special, Change Order Buyer Options", ""},
		{"Change Order #CO-12", "Change Order CO-12"},
	}
	for _, tt := range tests {
		rev := DetectRevision(pagesOf(trowSplit(700, tcell{50, tt.text})))
		if tt.want == "" {
			assert.Nil(t, rev, tt.text)
			continue
		}
		if assert.NotNil(t, rev, tt.text) {
			assert.Equal(t, tt.want, rev.Label)
		}
	}
}
