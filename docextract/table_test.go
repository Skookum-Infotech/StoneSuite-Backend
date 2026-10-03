package docextract

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func parse(t *testing.T, pages ...PageRows) tableResult {
	t.Helper()
	res, err := parseTable(pages, Limits{})
	require.NoError(t, err)
	return res
}

func TestParseTable_DetectHeader(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		ok   bool
	}{
		{"standard", stdHeader(500), true},
		{"vocabulary variants", trow(500, tcell{50, "Part #"}, tcell{120, "Desc."}, tcell{300, "Quantity"}, tcell{380, "Rate"}), true},
		{"two hits only", trow(500, tcell{50, "Description"}, tcell{300, "Qty"}), false},
		{"prose", trowSplit(500, tcell{50, "Please confirm the quantity and price"}), false},
		{"phrase titles", trow(500, tcell{50, "Item Description"}, tcell{300, "Qty"}, tcell{360, "Unit Price"}, tcell{440, "Line Total"}), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := detectTableHeader(tt.row)
			assert.Equal(t, tt.ok, ok)
		})
	}
}

func TestParseTable_BasicAndTotalsStop(t *testing.T) {
	res := parse(t, PageRows{Page: 1, Rows: []Row{
		trowSplit(600, tcell{50, "PO # 1"}),
		stdHeader(560),
		stdLine(544, "A-1", "Granite slab", "10", "SF", "$12.50", "$125.00"),
		stdLine(528, "A-2", "Quartz slab", "4", "EA", "$450.00", "$1,800.00"),
		trow(500, tcell{420, "Subtotal"}, tcell{500, "$1,925.00"}),
		stdLine(484, "ZZ", "Should not be read", "1", "EA", "$1.00", "$1.00"),
	}})
	require.True(t, res.Found)
	require.Len(t, res.Lines, 2)
	l := res.Lines[0]
	assert.Equal(t, "A-1", l.SKU.Value)
	assert.Equal(t, "Granite slab", l.Description.Value)
	assert.Equal(t, int64(10000), l.QtyMilli)
	assert.Equal(t, Cents(1250), l.UnitPriceCents)
	assert.Equal(t, Cents(12500), l.AmountCents)
	assert.Equal(t, "SF", l.UoM.Value)
	assert.Empty(t, l.Flags)
	assert.Equal(t, [2]int{1, 3}, res.Spans[1])
	assert.Equal(t, []string{"sku", "desc", "qty", "uom", "price", "amount"}, res.Roles)
}

func TestParseTable_WrappedAndNotes(t *testing.T) {
	res := parse(t, PageRows{Page: 1, Rows: []Row{
		stdHeader(560),
		stdLine(544, "A-1", "Imperial White marble countertop", "8", "EA", "$620.00", "$4,960.00"),
		trow(532, tcell{110, "honed finish, two slabs"}),
		stdLine(516, "A-2", "Carrara vanity top", "3", "EA", "$275.00", "$825.00"),
		trow(504, tcell{110, "Note: call before delivery"}),
		trow(470, tcell{50, "Delivery to rear gate only"}),
	}})
	require.Len(t, res.Lines, 2)
	assert.Equal(t, "Imperial White marble countertop honed finish, two slabs", res.Lines[0].Description.Value)
	assert.Equal(t, "Carrara vanity top", res.Lines[1].Description.Value)
	require.Len(t, res.Notes, 2)
	assert.Equal(t, KindNote, res.Notes[0].Kind)
	assert.Contains(t, res.Notes[0].Description.Value, "call before delivery")
}

func TestParseTable_RepeatedHeaderMultiPage(t *testing.T) {
	p1 := PageRows{Page: 1, Rows: []Row{
		stdHeader(560),
		stdLine(544, "A-1", "One", "1", "EA", "$10.00", "$10.00"),
		stdLine(528, "A-2", "Two", "2", "EA", "$10.00", "$20.00"),
		trow(40, tcell{280, "Page 1 of 2"}),
	}}
	p2 := PageRows{Page: 2, Rows: []Row{
		stdHeader(740),
		stdLine(724, "A-3", "Three", "3", "EA", "$10.00", "$30.00"),
		trow(700, tcell{420, "Total"}, tcell{500, "$60.00"}),
	}}
	res := parse(t, p1, p2)
	require.Len(t, res.Lines, 3)
	assert.Equal(t, "Three", res.Lines[2].Description.Value)
	assert.Equal(t, 2, res.Lines[2].Description.Page)
	assert.Empty(t, res.Notes, "footer and repeated header must not become notes")
	assert.Equal(t, [2]int{0, 1}, res.Spans[2])
}

func TestParseTable_LineKinds(t *testing.T) {
	res := parse(t, PageRows{Page: 1, Rows: []Row{
		stdHeader(560),
		stdLine(544, "GR-1", "Island top", "1", "EA", "$2,400.00", "$2,400.00"),
		trow(528, tcell{124, "Eased edge profile"}, tcell{330, "1"}, tcell{362, "EA"}, tcell{420, "$150.00"}, tcell{500, "$150.00"}),
		trow(512, tcell{110, "+ Sink hole"}, tcell{330, "1"}, tcell{362, "EA"}, tcell{420, "$85.00"}, tcell{500, "$85.00"}),
		trow(496, tcell{110, "Sealer coat"}, tcell{330, "1"}, tcell{362, "EA"}, tcell{420, "$20.00"}, tcell{500, "$20.00"}),
		stdLine(480, "GR-2", "Wall top", "1", "EA", "$1,000.00", "$1,000.00"),
		trow(464, tcell{110, "w/ polished return"}, tcell{330, "1"}, tcell{420, "$5.00"}, tcell{500, "$5.00"}),
		trow(448, tcell{110, "Freight"}, tcell{500, "$150.00"}),
		trow(432, tcell{110, "Discount"}, tcell{500, "-$50.00"}),
		trow(416, tcell{110, "Deposit"}, tcell{500, "$200.00"}),
	}})
	var kinds []LineKind
	var parents []int
	for _, l := range res.Lines {
		kinds = append(kinds, l.Kind)
		parents = append(parents, l.ParentLine)
	}
	assert.Equal(t, []LineKind{KindProduct, KindAddon, KindAddon, KindAddon, KindProduct, KindAddon}, kinds)
	assert.Equal(t, []int{0, 1, 1, 1, 0, 5}, parents)
	require.Len(t, res.Charges, 3)
	assert.Equal(t, Cents(15000), res.Charges[0].AmountCents)
	assert.Equal(t, Cents(-5000), res.Charges[1].AmountCents)
}

func TestParseTable_AddonWithoutParentStaysProduct(t *testing.T) {
	res := parse(t, PageRows{Page: 1, Rows: []Row{
		stdHeader(560),
		stdLine(544, "", "Sink cutout", "1", "EA", "$85.00", "$85.00"),
	}})
	require.Len(t, res.Lines, 1)
	assert.Equal(t, KindProduct, res.Lines[0].Kind)
}

func TestParseTable_NumberFlags(t *testing.T) {
	tests := []struct {
		name string
		row  Row
		flag string
	}{
		{"garbled money", stdLine(544, "A", "x", "1", "EA", "$1,2O0.00", "$10.00"), FlagPriceUnparsed},
		{"EU decimal", stdLine(544, "A", "x", "1", "EA", "$10.00", "1.234,56"), FlagEUDecimal},
		{"zero qty", stdLine(544, "A", "x", "0", "EA", "$10.00", "$0.00"), FlagQtyRange},
		{"absurd qty", stdLine(544, "A", "x", "5,000,000", "EA", "$1.00", "$5.00"), FlagQtyRange},
		{"garbled qty", stdLine(544, "A", "x", "1,2O0", "EA", "$1.00", "$5.00"), FlagQtyUnparsed},
		{"negative qty", stdLine(544, "A", "x", "-2", "EA", "$1.00", "$2.00"), FlagQtyRange},
		{"amount mismatch", stdLine(544, "A", "x", "2", "EA", "$10.00", "$25.00"), FlagAmountMismatch},
		{"negative price", stdLine(544, "A", "x", "1", "EA", "-$10.00", "-$10.00"), FlagNegativePrice},
		{"missing amount", trow(544, tcell{50, "A"}, tcell{110, "x"}, tcell{330, "1"}, tcell{420, "$10.00"}), FlagAmountMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := parse(t, PageRows{Page: 1, Rows: []Row{stdHeader(560), tt.row}})
			require.Len(t, res.Lines, 1)
			assert.Contains(t, res.Lines[0].Flags, tt.flag)
		})
	}
}

func TestParseTable_QtyWithEmbeddedUnit(t *testing.T) {
	hdr := trow(560, tcell{50, "Item"}, tcell{110, "Description"}, tcell{330, "Qty"}, tcell{420, "Price"}, tcell{500, "Amount"})
	row := trow(544, tcell{50, "A"}, tcell{110, "Slab"}, tcell{330, "12.5 SF"}, tcell{420, "$10.00"}, tcell{500, "$125.00"})
	res := parse(t, PageRows{Page: 1, Rows: []Row{hdr, row}})
	require.Len(t, res.Lines, 1)
	assert.Equal(t, "12.5", res.Lines[0].Qty.Value)
	assert.Equal(t, "SF", res.Lines[0].UoM.Value)
}

func TestParseTable_LineCap(t *testing.T) {
	rows := []Row{stdHeader(700)}
	for i := 0; i < 5; i++ {
		rows = append(rows, stdLine(float64(684-16*i), "A", "x", "1", "EA", "$1.00", "$1.00"))
	}
	_, err := parseTable([]PageRows{{Page: 1, Rows: rows}}, Limits{MaxLines: 3})
	var ie *InputError
	require.True(t, errors.As(err, &ie))
	assert.Equal(t, FailLineCap, ie.Code)
}

func TestParseTable_NoTable(t *testing.T) {
	res := parse(t, PageRows{Page: 1, Rows: []Row{trowSplit(600, tcell{50, "Just a letter"})}})
	assert.False(t, res.Found)
	assert.Empty(t, res.Lines)
}

func TestPrunePages(t *testing.T) {
	terms := PageRows{Page: 2, Rows: []Row{
		trowSplit(700, tcell{50, "Terms and conditions of sale apply to all orders placed with the seller and may not be varied"}),
		trowSplit(686, tcell{50, "except in writing signed by an officer of the seller the buyer accepts these terms in full"}),
	}}
	dense := PageRows{Page: 3, Rows: []Row{
		trowSplit(700, tcell{50, "Subtotal $100.00 Tax $8.25 Total $108.25 Qty 4 10 20 30"}),
	}}
	tablePage := PageRows{Page: 4, Rows: []Row{stdHeader(700)}}
	first := PageRows{Page: 1, Rows: []Row{trowSplit(700, tcell{50, "Cover letter with no numbers at all"})}}
	got := PrunePages([]PageRows{first, terms, dense, tablePage})
	var nums []int
	for _, p := range got {
		nums = append(nums, p.Page)
	}
	assert.Equal(t, []int{1, 3, 4}, nums, "terms page dropped, original page numbers kept")
}
