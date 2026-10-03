//go:build dbtest

package docextractjob

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
)

func TestResolveCustomer(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniq()
	acme := seedCustomer(t, pool, "Acme"+tag+" Stone", "CACT")
	held := seedCustomer(t, pool, "Borealis"+tag+" Tile", "CCHD")
	seedCustomer(t, pool, "Granite"+tag+" Works East", "CACT")
	seedCustomer(t, pool, "Granite"+tag+" Works West", "CACT")
	seedCustomer(t, pool, "Ourco"+tag, "CACT") // the tenant's own name also exists as a customer row
	r := NewResolver(pool, ResolveOptions{OwnCompanyNames: []string{"Ourco" + tag}})

	t.Run("exact normalized name", func(t *testing.T) {
		m, err := r.ResolveCustomer(ctx, "  ACME"+tag+"   stone ")
		require.NoError(t, err)
		assert.Equal(t, acme, m.UUID)
		assert.True(t, m.Active)
		assert.Equal(t, docextract.ConfHigh, m.Confidence)
		assert.Equal(t, docextract.SourceDocument, m.Source)
	})
	t.Run("inactive customer is flagged not hidden", func(t *testing.T) {
		m, err := r.ResolveCustomer(ctx, "Borealis"+tag+" Tile")
		require.NoError(t, err)
		assert.Equal(t, held, m.UUID)
		assert.False(t, m.Active)
	})
	t.Run("fuzzy picks a clear winner as check", func(t *testing.T) {
		m, err := r.ResolveCustomer(ctx, "Acme"+tag+" Stone Inc")
		require.NoError(t, err)
		assert.Equal(t, acme, m.UUID)
		assert.Equal(t, docextract.ConfCheck, m.Confidence)
		require.NotEmpty(t, m.Candidates)
		assert.Equal(t, acme, m.Candidates[0].UUID)
	})
	t.Run("ambiguous fuzzy returns candidates unpicked", func(t *testing.T) {
		m, err := r.ResolveCustomer(ctx, "Granite"+tag+" Works")
		require.NoError(t, err)
		assert.Empty(t, m.UUID)
		assert.Equal(t, docextract.ConfNotFound, m.Confidence)
		assert.Len(t, m.Candidates, 2)
	})
	t.Run("own company is never the customer", func(t *testing.T) {
		m, err := r.ResolveCustomer(ctx, "Ourco"+tag+" Inc")
		require.NoError(t, err)
		assert.Empty(t, m.UUID)
		assert.Empty(t, m.Candidates)
		m, err = r.ResolveCustomer(ctx, "Ourco"+tag)
		require.NoError(t, err)
		assert.Empty(t, m.UUID, "the exact own-company name is not a customer")
	})
	t.Run("LIKE metacharacters are inert", func(t *testing.T) {
		m, err := r.ResolveCustomer(ctx, "%%__")
		require.NoError(t, err)
		assert.Empty(t, m.UUID)
		assert.Empty(t, m.Candidates)
	})
	t.Run("learned alias wins and is marked learned", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO document_party_alias (party_kind, alias_key, party_uuid) VALUES ('customer', 'a.s. holdings', $1::uuid)`, acme)
		require.NoError(t, err)
		m, err := r.ResolveCustomer(ctx, "A.S.  Holdings")
		require.NoError(t, err)
		assert.Equal(t, acme, m.UUID)
		assert.Equal(t, docextract.SourceLearned, m.Source)
		assert.Equal(t, docextract.ConfHigh, m.Confidence)
	})
}

func TestResolvePaymentTerms(t *testing.T) {
	pool := testPool(t)
	r := NewResolver(pool, ResolveOptions{})
	ctx := context.Background()
	for _, in := range []string{"Net 30", "net30", "  NET   30 "} {
		m, err := r.ResolvePaymentTerms(ctx, in)
		require.NoError(t, err)
		require.NotNil(t, m, in)
		assert.Equal(t, "Net 30", m.Name)
	}
	m, err := r.ResolvePaymentTerms(ctx, "Net 31")
	require.NoError(t, err)
	assert.Nil(t, m)
}

func TestResolveItems(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniq()
	skuA, skuI := "SLB"+tag+"-1", "OLD"+tag
	slab := seedItem(t, pool, skuA, "Calacatta"+tag, "SQFT", 5000, true)
	old := seedItem(t, pool, skuI, "Old stone"+tag, "EA", 100, false)
	other := seedItem(t, pool, "ALT"+tag, "Alt thing"+tag, "EA", 100, true)
	cust := seedCustomer(t, pool, "Itemco"+tag, "CACT")
	_, err := pool.Exec(ctx, `INSERT INTO document_item_alias (party_uuid, alias_key, item_uuid) VALUES ($1::uuid, $2, $3::uuid)`,
		cust, "sku:vendor77", other)
	require.NoError(t, err)

	r := NewResolver(pool, ResolveOptions{})
	line := func(sku, desc, uom string, qty, price int64) docextract.Line {
		return itemLine(sku, desc, uom, qty, price)
	}
	lines := []docextract.Line{
		line(skuA, "", "sqft", 2000, 5000),                 // 0 exact sku
		line("", "calacatta"+tag, "sqm", 10000, 5382),      // 1 name + unit conversion
		line(skuI, "", "ea", 1000, 100),                    // 2 inactive
		line("VENDOR-77", "", "ea", 1000, 100),             // 3 learned alias (cust-scoped)
		line("NOPE-1", "nothing like it", "ea", 1000, 100), // 4 unmatched
		{Kind: docextract.KindCharge},                      // 5 charge: no match entry
		line(skuA+"-3CM", "", "sqft", 1000, 5000),          // 6 variant never matches base
		line(skuA, "", "sqft", 1000, 5600),                 // 7 price differs
	}
	got, err := r.ResolveItems(ctx, cust, lines)
	require.NoError(t, err)
	byIdx := map[int]LineMatch{}
	for _, m := range got {
		byIdx[m.Index] = m
	}
	assert.NotContains(t, byIdx, 5, "charge lines are not item lines")

	require.NotNil(t, byIdx[0].Item)
	assert.Equal(t, slab, byIdx[0].Item.UUID)
	assert.Equal(t, MatchSKU, byIdx[0].MatchedBy)
	assert.Equal(t, int64(5000), byIdx[0].Item.CatalogPriceCents)
	assert.Equal(t, "SQFT", byIdx[0].Item.UnitCode)
	assert.Equal(t, "area", byIdx[0].Item.UnitCategory)
	assert.Empty(t, byIdx[0].Flags)

	require.NotNil(t, byIdx[1].Item)
	assert.Equal(t, MatchName, byIdx[1].MatchedBy)
	require.NotNil(t, byIdx[1].Converted)
	assert.Equal(t, "SQFT", byIdx[1].Converted.ToUoM)

	require.NotNil(t, byIdx[2].Item)
	assert.Equal(t, old, byIdx[2].Item.UUID)
	assert.Contains(t, byIdx[2].Flags, LineFlagItemInactive)

	require.NotNil(t, byIdx[3].Item)
	assert.Equal(t, other, byIdx[3].Item.UUID)
	assert.Equal(t, MatchAlias, byIdx[3].MatchedBy)
	assert.Equal(t, docextract.SourceLearned, byIdx[3].Source)

	assert.Nil(t, byIdx[4].Item)
	assert.Equal(t, []string{LineFlagItemUnmatched}, byIdx[4].Flags)
	assert.Nil(t, byIdx[6].Item)
	require.NotNil(t, byIdx[7].Item)
	assert.Contains(t, byIdx[7].Flags, LineFlagPriceDiffers)

	// The alias is scoped to its party: another customer does not get it.
	got2, err := r.ResolveItems(ctx, "", lines[3:4])
	require.NoError(t, err)
	assert.Nil(t, got2[0].Item)
}
