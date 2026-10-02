package docextractjob

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
)

// itemLine builds a product line.
func itemLine(sku, desc, uom string, qtyMilli, priceCents int64) docextract.Line {
	f := func(v string) docextract.Field {
		if v == "" {
			return docextract.Field{}
		}
		return docextract.Field{Value: v}
	}
	return docextract.Line{
		Kind: docextract.KindProduct, SKU: f(sku), Description: f(desc), UoM: f(uom),
		UnitPrice: docextract.Field{Value: "x"}, QtyMilli: qtyMilli, UnitPriceCents: docextract.Cents(priceCents),
	}
}

func testItem(uuid, sku, name, unitCode, unitName, cat string, price int64, active bool) *itemRow {
	return &itemRow{
		info:     ItemInfo{UUID: uuid, Name: name, SKU: sku, UnitCode: unitCode, UnitCategory: cat, CatalogPriceCents: price, Active: active},
		unitName: unitName, skuKey: NormalizeSKU(sku), nameKey: name,
	}
}

func buildIndex(items ...*itemRow) itemIndex {
	idx := itemIndex{byUUID: map[string]*itemRow{}, bySKU: map[string][]*itemRow{}, byName: map[string][]*itemRow{}}
	for _, it := range items {
		idx.byUUID[it.info.UUID] = it
		idx.bySKU[it.skuKey] = append(idx.bySKU[it.skuKey], it)
		idx.byName[it.nameKey] = append(idx.byName[it.nameKey], it)
	}
	return idx
}

func TestMatchLine(t *testing.T) {
	slab := testItem(itemA, "SLAB-1", "calacatta gold", "SQFT", "Square Foot", "area", 5000, true)
	slab3 := testItem(itemB, "SLAB-1-3CM", "calacatta gold 3cm", "SQFT", "Square Foot", "area", 6000, true)
	inactive := testItem("cccccccc-cccc-4ccc-8ccc-cccccccccccc", "OLD-1", "old stone", "EA", "Each", "count", 100, false)
	dupA := testItem("dddddddd-dddd-4ddd-8ddd-dddddddddddd", "AB1", "twin a", "EA", "Each", "count", 100, true)
	dupB := testItem("eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee", "A-B-1", "twin b", "EA", "Each", "count", 100, true)
	idx := buildIndex(slab, slab3, inactive, dupA, dupB)

	tests := []struct {
		name      string
		line      docextract.Line
		aliases   map[string]string
		wantItem  string
		wantBy    string
		wantSrc   docextract.Source
		wantFlags []string
	}{
		{"exact sku, same price", itemLine("slab 1", "", "sqft", 1000, 5000), nil, itemA, MatchSKU, docextract.SourceCatalog, nil},
		{"variant sku never matches base", itemLine("SLAB-1-3CM", "", "sqft", 1000, 6000), nil, itemB, MatchSKU, docextract.SourceCatalog, nil},
		{"unknown variant is unmatched", itemLine("SLAB-1-POL", "", "sqft", 1000, 5000), nil, "", "", "", []string{LineFlagItemUnmatched}},
		{"name match", itemLine("", "Calacatta  Gold", "sqft", 1000, 5000), nil, itemA, MatchName, docextract.SourceCatalog, nil},
		{"price differs keeps doc price and flags", itemLine("SLAB-1", "", "sqft", 1000, 5500), nil, itemA, MatchSKU, docextract.SourceCatalog, []string{LineFlagPriceDiffers}},
		{"inactive item flagged", itemLine("OLD-1", "", "ea", 1000, 100), nil, inactive.info.UUID, MatchSKU, docextract.SourceCatalog, []string{LineFlagItemInactive}},
		{"ambiguous normalized sku", itemLine("ab-1", "", "ea", 1000, 100), nil, "", "", "", []string{LineFlagItemAmbiguous}},
		{"alias beats sku", itemLine("SLAB-1", "", "sqft", 1000, 6000), map[string]string{"sku:slab1": itemB}, itemB, MatchAlias, docextract.SourceLearned, nil},
		{"alias to missing item falls back to sku", itemLine("SLAB-1", "", "sqft", 1000, 5000), map[string]string{"sku:slab1": "ffffffff-ffff-4fff-8fff-ffffffffffff"}, itemA, MatchSKU, docextract.SourceCatalog, nil},
		{"uom unknown flagged", itemLine("SLAB-1", "", "bundles", 1000, 5000), nil, itemA, MatchSKU, docextract.SourceCatalog, []string{LineFlagUoMUnknown}},
		{"cross category flagged", itemLine("SLAB-1", "", "kg", 1000, 5000), nil, itemA, MatchSKU, docextract.SourceCatalog, []string{LineFlagUoMMismatch}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := matchLine(0, tc.line, idx, tc.aliases)
			if tc.wantItem == "" {
				assert.Nil(t, m.Item)
			} else {
				require.NotNil(t, m.Item)
				assert.Equal(t, tc.wantItem, m.Item.UUID)
			}
			assert.Equal(t, tc.wantBy, m.MatchedBy)
			assert.Equal(t, tc.wantSrc, m.Source)
			assert.Equal(t, tc.wantFlags, m.Flags)
		})
	}
}

func TestMatchLineConvertsUnitsPreservingAmount(t *testing.T) {
	slab := testItem(itemA, "SLAB-1", "calacatta gold", "SQFT", "Square Foot", "area", 1000, true)
	idx := buildIndex(slab)
	// 10 sqm at $107.64/sqm == 107.6391 sqft at about $10.00/sqft.
	m := matchLine(0, itemLine("SLAB-1", "", "sqm", 10000, 10764), idx, nil)
	require.NotNil(t, m.Converted)
	assert.Equal(t, "sqm", m.Converted.FromUoM)
	assert.Equal(t, "SQFT", m.Converted.ToUoM)
	assert.Equal(t, int64(10000), m.Converted.FromQtyMilli)
	assert.InDelta(t, 107639, m.Converted.QtyMilli, 2)
	assert.InDelta(t, 1000, m.Converted.UnitPriceCents, 1)
	assert.NotContains(t, m.Flags, LineFlagUoMMismatch)
	assert.NotContains(t, m.Flags, LineFlagPriceDiffers, "converted price is compared with the catalog")
}

func TestMatchLineSameCategoryButUnconvertibleItemUnit(t *testing.T) {
	box := testItem(itemA, "BX-1", "box item", "BOX", "Box", "count", 100, true)
	m := matchLine(0, itemLine("BX-1", "", "ea", 1000, 100), buildIndex(box), nil)
	assert.Nil(t, m.Converted)
	assert.Equal(t, []string{LineFlagUoMMismatch}, m.Flags)
}

func TestMatchLineNoUoMIsNotFlagged(t *testing.T) {
	slab := testItem(itemA, "SLAB-1", "calacatta gold", "SQFT", "Square Foot", "area", 5000, true)
	m := matchLine(0, itemLine("SLAB-1", "", "", 1000, 5000), buildIndex(slab), nil)
	assert.Empty(t, m.Flags)
	assert.Nil(t, m.Converted)
}
