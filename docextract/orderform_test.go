package docextract

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func words(ss ...string) []Word {
	out := make([]Word, len(ss))
	for i, s := range ss {
		out[i] = Word{X: float64(i) * 40, W: 30, Text: s}
	}
	return out
}

func TestMatchLabel(t *testing.T) {
	tests := []struct {
		name    string
		run     []Word
		wantKey string
		wantN   int
	}{
		{"single word", words("Color", "x"), ofColor, 1},
		{"two words", words("Install", "Date"), ofInstallDate, 2},
		{"split inside a word", words("Si", "nk", "Model"), ofSink, 3},
		{"slash ignored", words("City", "/", "Zip"), ofCityZip, 3},
		{"hyphen ignored", words("Pop-up", "Outlet"), ofPopUp, 2},
		{"long label with run-in hint text", words("Special", "Instructio*n*sF"), ofSpecial, 2},
		{"short label never matches as a prefix", words("Planner"), "", 0},
		{"not a label", words("Something"), "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, n := matchLabel(tt.run)
			assert.Equal(t, tt.wantKey, key)
			assert.Equal(t, tt.wantN, n)
		})
	}
}

func TestHeadingLen(t *testing.T) {
	wide := []Word{{X: 42, W: 45, Text: "KITCHEN"}, {X: 312, W: 40, Text: "ISLAND"}}
	tests := []struct {
		name string
		run  []Word
		want int
	}{
		{"area word", words("KITCHEN"), 1},
		{"multi-word heading", words("PRIMARY", "/", "MASTER", "BATH"), 4},
		{"numbered", words("BATH", "2"), 2},
		{"stops at a label", words("POWDER", "1", "Location"), 2},
		{"custom area", words("CUSTOM", "AREA:"), 2},
		{"next column's heading is not part of it", wide, 1},
		{"title case is a label, not a heading", words("Kitchen"), 0},
		{"a title is not an area", words("COUNTERTOP", "ORDER", "FORM"), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, headingLen(tt.run))
		})
	}
}

func TestFaucetPositions(t *testing.T) {
	marks := []Word{{X: 48, W: 8, Text: `8"`}, {X: 89, W: 8, Text: `4"`}, {X: 168, W: 6, Text: "C"}}
	ticks := []Word{{X: 86, W: 10, Text: CheckedMark}, {X: 164, W: 10, Text: CheckedMark}, {X: 300, W: 10, Text: CheckedMark}}
	assert.Equal(t, `4" C`, faucetPositions(ticks, marks), "a tick with no mark nearby is ignored")
}

func TestCustomerFromEmail(t *testing.T) {
	tests := []struct{ email, want string }{
		{"tyler.cohn@highlandhomes.com", "Highland Homes"},
		{"a@mail.perrybuilders.net", "Perry Builders"},
		{"a@acme.com", "Acme"},
		{"someone@gmail.com", ""},
		{"no-at-sign", ""},
		{"a@x123.com", ""},
		{"a@homes.com", "Homes"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, customerFromEmail(tt.email), tt.email)
	}
}

func TestAreaDetail(t *testing.T) {
	v := map[string]ofValue{
		ofFinish: {Text: "POLISHED"}, ofEdge: {Text: "FLAT"}, ofBacksplash: {Text: CheckedMark}, ofHeight: {Text: `4"`},
		ofFaucet: {Text: `4" C 4"`}, ofHalfWall: {Text: CheckedMark}, ofNotes: {Text: "KNEE WALL"}, ofQty: {Text: "2"},
	}
	assert.Equal(t, `Bath 2 — Backsplash 4"; Finish POLISHED; Edge FLAT; Faucet spread 4" C 4"; Half wall; Notes KNEE WALL; Waterfall qty 2`,
		areaDetail("Bath 2", v))
	assert.Equal(t, "Kitchen", areaDetail("Kitchen", map[string]ofValue{}))
}

func TestAreaLines(t *testing.T) {
	t.Run("empty area has no line", func(t *testing.T) {
		assert.Empty(t, areaLines(&ofArea{Name: "PANTRY", Values: map[string]ofValue{}}, 0))
	})
	t.Run("sink only: area-named product plus the sink", func(t *testing.T) {
		ls := areaLines(&ofArea{Name: "UTILITY", Values: map[string]ofValue{ofSink: {Text: "20023-NA SINGLE STAINLESS"}}}, 3)
		require.Len(t, ls, 2)
		assert.Equal(t, "Utility", ls[0].Description.Value)
		assert.Equal(t, ConfCheck, ls[0].Description.Confidence)
		assert.Equal(t, KindAddon, ls[1].Kind)
		assert.Equal(t, 4, ls[1].ParentLine)
		assert.Equal(t, "20023-NA", ls[1].SKU.Value)
	})
	t.Run("sink without a model number has no sku", func(t *testing.T) {
		ls := areaLines(&ofArea{Name: "BAR", Values: map[string]ofValue{ofColor: {Text: "MISTERIO"}, ofSink: {Text: "CUSTOMER SUPPLIED"}}}, 0)
		require.Len(t, ls, 2)
		assert.Empty(t, ls[1].SKU.Value)
	})
}

func TestOrderForm_Fixture(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(fixtureDir, "14_countertop_order_form.pdf"))
	require.NoError(t, err)
	llm := &fakeLLM{reply: `{"customer_name":"Dana Reyes","po_number":"CT-3648"}`}
	res, method, err := Extract(context.Background(), b, DocTypeSalesOrder, Options{LLM: llm, OwnCompanyNames: []string{"Elevation Stone"}})
	require.NoError(t, err)

	assert.Equal(t, MethodParser, method)
	assert.Zero(t, llm.calls, "an order form never asks the model")
	assert.Equal(t, ClassOrderForm, res.ClassifiedAs)
	assert.Equal(t, ConfCheck, res.Header.CustomerName.Confidence, "a customer guessed from an email domain needs review")
	assert.Contains(t, res.Warnings, WarnNoPrices)
	assert.Contains(t, res.Warnings, WarnMissingPONumber)
	assert.NotContains(t, res.Warnings, WarnMissingOrderDate)
	assert.NotContains(t, res.Warnings, WarnNotRecognized)

	require.Len(t, res.Lines, 3)
	assert.Equal(t, `Kitchen — Backsplash 4"; Finish POLISHED; Edge EASED; Cooktop cutout CT-3648`, res.Lines[0].Detail)
	assert.Equal(t, "Island — Finish POLISHED; Edge HALF BULLNOSE; Faucet spread C", res.Lines[1].Detail)
	assert.Equal(t, 2, res.Lines[2].ParentLine)
	assert.Equal(t, int64(qtyOneMilli), res.Lines[0].QtyMilli)
	assert.Equal(t, 1, res.Lines[0].Description.Page)
	assert.NotZero(t, res.Lines[0].Description.Row)
}

func TestOrderForm_UnfilledFormIsNotParsedAsOne(t *testing.T) {
	// The printed form alone (nothing typed) has no values to read.
	pages := []PageRows{{Page: 1, Rows: []Row{
		{Y: 736, Words: words("Builder", "Community", "Plan")},
		{Y: 711, Words: words("Phone", "Install", "Address")},
		{Y: 634, Words: words("KITCHEN")},
		{Y: 617, Words: words("Color")},
	}}}
	_, ok := parseOrderForm(pages)
	assert.False(t, ok)
}

func TestTitleWords(t *testing.T) {
	tests := []struct{ in, want string }{
		{"PRIMARY / MASTER BATH", "Primary / Master Bath"},
		{"ático ñandú", "Ático Ñandú"},
		{"", ""},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, titleWords(tt.in), tt.in)
	}
}

func TestOrderForm_LineCap(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(fixtureDir, "14_countertop_order_form.pdf"))
	require.NoError(t, err)
	_, _, err = Extract(context.Background(), b, DocTypeSalesOrder, Options{Limits: Limits{MaxLines: 2}})
	var ie *InputError
	require.ErrorAs(t, err, &ie)
	assert.Equal(t, FailLineCap, ie.Code)
}

func TestClassify_OrderFormTitleAloneIsNotAnOrderForm(t *testing.T) {
	// Only reading the form's fields makes it an order form; a priced PDF
	// headed "Order Form" still goes through the PO path (and the model).
	assert.Equal(t, "", Classify(pagesOf(trowSplit(750, tcell{50, "ORDER FORM"}))))
	assert.Equal(t, "", Classify(pagesOf(trowSplit(750, tcell{50, "COUNTERTOP ORDER FORM"}))))
}
