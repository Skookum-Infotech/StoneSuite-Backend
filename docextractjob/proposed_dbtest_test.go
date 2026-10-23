//go:build dbtest

package docextractjob

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
)

func TestResolveDoc_ProposedEntities_SalesOrder(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	u := uniq()
	seedCustomer(t, pool, "Known"+u+" Stone", "CACT")
	seedItem(t, pool, "KNOWN-"+u, "Known slab "+u, "SQFT", 1000, true)

	ex := newExtraction(t, s, "id-1")
	extracted := docextract.Result{DocType: docextract.DocTypeSalesOrder}
	extracted.Header.CustomerName = docextract.Field{Value: "Brandnew" + u + " Builders", Confidence: docextract.ConfHigh}
	extracted.Lines = []docextract.Line{
		{Kind: docextract.KindProduct, SKU: docextract.Field{Value: "KNOWN-" + u}, Description: docextract.Field{Value: "Known slab " + u}, UoM: docextract.Field{Value: "SF"}},
		{Kind: docextract.KindProduct, SKU: docextract.Field{Value: "NEW-" + u}, Description: docextract.Field{Value: "Zebra quartz " + u}, UoM: docextract.Field{Value: "SF"}},
	}

	doc, err := ResolveDoc(context.Background(), pool, ex, "sha-"+u, extracted, nil)
	require.NoError(t, err)
	require.NotNil(t, doc.ProposedEntities)
	kinds := map[string]string{}
	for _, e := range doc.ProposedEntities {
		kinds[e.Kind] = e.Name
	}
	assert.Equal(t, "Brandnew"+u+" Builders", kinds[ProposedKindCustomer])
	assert.Equal(t, "Zebra quartz "+u, kinds[ProposedKindItem])
	assert.Len(t, doc.ProposedEntities, 2)

	// Resolution and duplicates are exactly what the resolver/checker alone produce.
	want, err := NewResolver(pool, ResolveOptions{}).Resolve(context.Background(), extracted)
	require.NoError(t, err)
	if want.Lines == nil {
		want.Lines = []LineMatch{}
	}
	assert.JSONEq(t, mustJSON(t, want), mustJSON(t, doc.Resolution))
	assert.Empty(t, doc.Duplicates)

	// A known customer yields no customer proposal.
	extracted.Header.CustomerName = docextract.Field{Value: "Known" + u + " Stone"}
	doc, err = ResolveDoc(context.Background(), pool, ex, "sha2-"+u, extracted, nil)
	require.NoError(t, err)
	for _, e := range doc.ProposedEntities {
		assert.NotEqual(t, ProposedKindCustomer, e.Kind)
	}
}

func TestResolveDoc_ProposedEntities_Vendor(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	u := uniq()
	seedVendor(t, pool, "Seeded"+u+" Supply", "ACT_")

	ex := newExtraction(t, s, "id-1")
	ex.DocType = string(docextract.DocTypePurchaseOrder)
	extracted := docextract.Result{DocType: docextract.DocTypePurchaseOrder}
	extracted.Header.VendorName = docextract.Field{Value: "Unknown" + u + " Quarry"}

	doc, err := ResolveDoc(context.Background(), pool, ex, "sha-"+u, extracted, nil)
	require.NoError(t, err)
	require.Len(t, doc.ProposedEntities, 1)
	assert.Equal(t, ProposedKindVendor, doc.ProposedEntities[0].Kind)

	extracted.Header.VendorName = docextract.Field{Value: "Seeded" + u + " Supply"}
	doc, err = ResolveDoc(context.Background(), pool, ex, "sha2-"+u, extracted, nil)
	require.NoError(t, err)
	assert.Empty(t, doc.ProposedEntities)
	assert.NotNil(t, doc.ProposedEntities)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
