package docextractjob

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
)

func notFoundParty() PartyMatch { return PartyMatch{Confidence: docextract.ConfNotFound} }

func soResult(name string) docextract.Result {
	r := docextract.Result{DocType: docextract.DocTypeSalesOrder}
	r.Header.CustomerName = docextract.Field{Value: name, Confidence: docextract.ConfHigh, Page: 1, Row: 3}
	return r
}

func poResult(name string) docextract.Result {
	r := docextract.Result{DocType: docextract.DocTypePurchaseOrder}
	r.Header.VendorName = docextract.Field{Value: name, Confidence: docextract.ConfCheck, Page: 2, Row: 5}
	return r
}

func productLine(sku, desc string) docextract.Line {
	return docextract.Line{
		Kind: docextract.KindProduct, SKU: docextract.Field{Value: sku, Confidence: docextract.ConfCheck},
		Description:    docextract.Field{Value: desc, Confidence: docextract.ConfHigh, Page: 1, Row: 7},
		UoM:            docextract.Field{Value: "SF"},
		UnitPriceCents: 1250,
	}
}

func unmatched(i int) LineMatch {
	return LineMatch{Index: i, Flags: []string{LineFlagItemUnmatched}}
}

func TestDeriveProposedEntities_Parties(t *testing.T) {
	tests := []struct {
		name  string
		ex    docextract.Result
		res   Resolution
		own   []string
		kind  string
		want  int
		needs []string
	}{
		{"customer unknown", soResult("Acme Stoneworks"), Resolution{Customer: notFoundParty()}, nil, ProposedKindCustomer, 1, []string{NeedContactEmail}},
		{"vendor unknown on PO", poResult("Granite Co"), Resolution{Vendor: notFoundParty()}, nil, ProposedKindVendor, 1, nil},
		{"vendor unknown on bill", func() docextract.Result {
			r := poResult("Granite Co")
			r.DocType = docextract.DocTypeVendorBill
			return r
		}(),
			Resolution{Vendor: notFoundParty()}, nil, ProposedKindVendor, 1, nil},
		{"matched by alias", soResult("Acme"), Resolution{Customer: PartyMatch{UUID: "u1", Active: true, Confidence: docextract.ConfHigh}}, nil, "", 0, nil},
		{"matched but inactive", soResult("Acme"), Resolution{Customer: PartyMatch{UUID: "u1", Active: false, Confidence: docextract.ConfHigh}}, nil, "", 0, nil},
		{"ambiguous", soResult("Acme"), Resolution{Customer: PartyMatch{Confidence: docextract.ConfNotFound, Candidates: []Candidate{{UUID: "a", Score: 0.7}, {UUID: "b", Score: 0.6}}}}, nil, "", 0, nil},
		{"only weak candidates", soResult("Akhila Tailors Surat"), Resolution{Customer: PartyMatch{Confidence: docextract.ConfNotFound, Candidates: []Candidate{{UUID: "a", Score: 0.4}}}}, nil, ProposedKindCustomer, 1, []string{NeedContactEmail}},
		{"own company", soResult("Elevation  STONE LLC"), Resolution{Customer: notFoundParty()}, []string{"Elevation Stone LLC"}, "", 0, nil},
		{"not found field", soResult(""), Resolution{Customer: notFoundParty()}, nil, "", 0, nil},
		{"control chars only", soResult("\x00\x07"), Resolution{Customer: notFoundParty()}, nil, "", 0, nil},
		{"customer not proposed on PO", func() docextract.Result {
			r := poResult("X")
			r.Header.CustomerName = docextract.Field{Value: "Y"}
			return r
		}(),
			Resolution{Customer: notFoundParty(), Vendor: PartyMatch{UUID: "v"}}, nil, "", 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DeriveProposedEntities(tc.ex, tc.res, tc.own)
			require.Len(t, got, tc.want)
			if tc.want == 0 {
				return
			}
			e := got[0]
			assert.Equal(t, tc.kind, e.Kind)
			assert.Equal(t, tc.needs, e.Needs)
			assert.Equal(t, ProposedReasonNoMatch, e.UnresolvedReason)
			assert.Equal(t, string(tc.ex.DocType), e.DocType)
			assert.True(t, strings.HasPrefix(e.Key, "pe_"))
			assert.Len(t, e.Key, 3+12)
		})
	}
}

func TestDeriveProposedEntities_PartySource(t *testing.T) {
	got := DeriveProposedEntities(poResult("Granite Co"), Resolution{Vendor: notFoundParty()}, nil)
	require.Len(t, got, 1)
	assert.Equal(t, ProposedSource{Field: "vendorName", Page: 2, Row: 5}, got[0].Source)
	assert.Equal(t, docextract.ConfCheck, got[0].Confidence)
	assert.Equal(t, "Granite Co", got[0].Name)
}

func TestDeriveProposedEntities_KeyStable(t *testing.T) {
	a := DeriveProposedEntities(soResult("Acme Stoneworks"), Resolution{Customer: notFoundParty()}, nil)
	b := DeriveProposedEntities(soResult("  ACME   Stoneworks "), Resolution{Customer: notFoundParty()}, nil)
	c := DeriveProposedEntities(poResult("Acme Stoneworks"), Resolution{Vendor: notFoundParty()}, nil)
	require.Len(t, a, 1)
	require.Len(t, b, 1)
	require.Len(t, c, 1)
	assert.Equal(t, a[0].Key, b[0].Key)
	assert.NotEqual(t, a[0].Key, c[0].Key, "kind is part of the key")
}

func TestDeriveProposedEntities_Items(t *testing.T) {
	ex := soResult("")
	ex.Lines = []docextract.Line{
		productLine("AB-100", "Calacatta slab"),     // 0 unmatched
		productLine("ab 100", "Calacatta slab 3cm"), // 1 same sku -> merged
		productLine("", "Edge profile"),             // 2 desc only
		productLine("", "edge  PROFILE"),            // 3 same desc -> merged
		productLine("ZZ-1", "Ambiguous thing"),      // 4 ambiguous
		productLine("MM-1", "Matched thing"),        // 5 matched
		{Kind: docextract.KindNote},                 // 6 not an item line
		productLine("", ""),                         // 7 nothing to name it
		productLine("IN-1", "Inactive thing"),       // 8 inactive match (Item set)
	}
	res := Resolution{Lines: []LineMatch{
		unmatched(0), unmatched(1), unmatched(2), unmatched(3),
		{Index: 4, Flags: []string{LineFlagItemUnmatched, LineFlagItemAmbiguous}},
		{Index: 5, Item: &ItemInfo{UUID: "i"}},
		unmatched(7),
		{Index: 8, Item: &ItemInfo{UUID: "j"}, Flags: []string{LineFlagItemInactive}},
	}}
	got := DeriveProposedEntities(ex, res, nil)
	require.Len(t, got, 2)

	sku := got[0]
	assert.Equal(t, ProposedKindItem, sku.Kind)
	assert.Equal(t, "Calacatta slab", sku.Name)
	assert.Equal(t, ProposedFields{SKU: "AB-100", Description: "Calacatta slab", UoM: "SF", UnitPriceCents: 1250}, sku.Fields)
	assert.Equal(t, []int{0, 1}, sku.Source.Lines)
	assert.Equal(t, "lines", sku.Source.Field)
	assert.Equal(t, 1, sku.Source.Page)
	assert.Equal(t, 7, sku.Source.Row)
	assert.Empty(t, sku.Needs)
	assert.Equal(t, docextract.ConfHigh, sku.Confidence)

	desc := got[1]
	assert.Equal(t, "Edge profile", desc.Name)
	assert.Equal(t, []int{2, 3}, desc.Source.Lines)
	assert.Equal(t, []string{NeedSKU}, desc.Needs)
	assert.NotEqual(t, sku.Key, desc.Key)
}

func TestDeriveProposedEntities_SKUOnlyItem(t *testing.T) {
	ex := soResult("")
	ex.Lines = []docextract.Line{productLine("QZ-9", "")}
	got := DeriveProposedEntities(ex, Resolution{Lines: []LineMatch{unmatched(0)}}, nil)
	require.Len(t, got, 1)
	assert.Equal(t, "QZ-9", got[0].Name)
	assert.Empty(t, got[0].Needs)
}

func TestDeriveProposedEntities_Sanitizes(t *testing.T) {
	long := strings.Repeat("é", 400)
	ex := soResult("Acme\x00 \u202eCorp\x07 " + long)
	ex.Lines = []docextract.Line{productLine("S\x00K "+strings.Repeat("9", 100), "De\x01sc "+strings.Repeat("d", 900))}
	ex.Lines[0].UoM.Value = strings.Repeat("u", 50)
	got := DeriveProposedEntities(ex, Resolution{Customer: notFoundParty(), Lines: []LineMatch{unmatched(0)}}, nil)
	require.Len(t, got, 2)
	assert.True(t, strings.HasPrefix(got[0].Name, "Acme Corp"))
	assert.LessOrEqual(t, len([]rune(got[0].Name)), 100)
	assert.NotContains(t, got[0].Name, "\x00")
	assert.NotContains(t, got[0].Name, "\u202e")
	f := got[1].Fields
	assert.LessOrEqual(t, len([]rune(got[1].Name)), 150)
	assert.LessOrEqual(t, len([]rune(f.SKU)), 50)
	assert.LessOrEqual(t, len([]rune(f.Description)), 500)
	assert.LessOrEqual(t, len([]rune(f.UoM)), 20)
	assert.True(t, strings.HasPrefix(f.SKU, "SK 99") || strings.HasPrefix(f.SKU, "SK"), f.SKU)
	assert.NotContains(t, f.Description, "\x01")
}

func TestDeriveProposedEntities_EmptyIsNonNilSlice(t *testing.T) {
	got := DeriveProposedEntities(docextract.Result{DocType: docextract.DocTypeSalesOrder}, Resolution{}, nil)
	require.NotNil(t, got)
	assert.Empty(t, got)
}

func TestDeriveProposedEntities_SkipsJunkNames(t *testing.T) {
	tests := []struct {
		name string
		res  docextract.Result
		want int
	}{
		{"phone-like customer", soResult("Support: 1800-889-9991"), 0},
		{"bare phone number customer", soResult("1800-889-9991"), 0},
		{"digits-only vendor", poResult("123456"), 0},
		{"real customer", soResult("Prasanna Kumar"), 1},
		{"long digit run inside a name", soResult("Brandnew1792877753021104529 Builders"), 1},
		{"name with digits", soResult("7-Eleven Stores"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DeriveProposedEntities(tt.res, Resolution{Customer: notFoundParty(), Vendor: notFoundParty()}, nil)
			assert.Len(t, got, tt.want)
		})
	}

	r := soResult("")
	r.Lines = []docextract.Line{productLine("996819", "996819"), productLine("", "996819"), productLine("A-1", "Granite")}
	got := DeriveProposedEntities(r, Resolution{Lines: []LineMatch{unmatched(0), unmatched(1), unmatched(2)}}, nil)
	require.Len(t, got, 1, "an item whose name has no letters is not proposed")
	assert.Equal(t, "Granite", got[0].Name)
}
