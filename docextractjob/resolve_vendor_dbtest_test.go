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

func TestResolveVendor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniq()
	acme := seedVendor(t, pool, "Acme"+tag+" Supply", "ACT_")
	dormant := seedVendor(t, pool, "Dormant"+tag+" Quarry", "INA_")
	seedVendor(t, pool, "Granite"+tag+" Works East", "ACT_")
	seedVendor(t, pool, "Granite"+tag+" Works West", "ACT_")
	seedVendor(t, pool, "Ourco"+tag, "ACT_") // the tenant's own name also exists as a vendor row
	seedCustomer(t, pool, "Custonly"+tag, "CACT")
	r := NewResolver(pool, ResolveOptions{OwnCompanyNames: []string{"Ourco" + tag}})

	t.Run("exact normalized name", func(t *testing.T) {
		m, err := r.ResolveVendor(ctx, "  ACME"+tag+"   supply ")
		require.NoError(t, err)
		assert.Equal(t, acme, m.UUID)
		assert.True(t, m.Active)
		assert.Equal(t, docextract.ConfHigh, m.Confidence)
		assert.Equal(t, docextract.SourceDocument, m.Source)
	})
	t.Run("inactive vendor is flagged not hidden", func(t *testing.T) {
		m, err := r.ResolveVendor(ctx, "Dormant"+tag+" Quarry")
		require.NoError(t, err)
		assert.Equal(t, dormant, m.UUID)
		assert.False(t, m.Active)
		assert.Equal(t, "Inactive", m.StatusName)
	})
	t.Run("fuzzy picks a clear winner as check", func(t *testing.T) {
		m, err := r.ResolveVendor(ctx, "Acme"+tag+" Supply Inc")
		require.NoError(t, err)
		assert.Equal(t, acme, m.UUID)
		assert.Equal(t, docextract.ConfCheck, m.Confidence)
		require.NotEmpty(t, m.Candidates)
	})
	t.Run("ambiguous fuzzy returns candidates unpicked", func(t *testing.T) {
		m, err := r.ResolveVendor(ctx, "Granite"+tag+" Works")
		require.NoError(t, err)
		assert.Empty(t, m.UUID)
		assert.Len(t, m.Candidates, 2)
	})
	t.Run("own company is never the vendor", func(t *testing.T) {
		for _, name := range []string{"Ourco" + tag, "Ourco" + tag + " Inc"} {
			m, err := r.ResolveVendor(ctx, name)
			require.NoError(t, err)
			assert.Empty(t, m.UUID, name)
			assert.Empty(t, m.Candidates, name)
		}
	})
	t.Run("a customer is not a vendor", func(t *testing.T) {
		m, err := r.ResolveVendor(ctx, "Custonly"+tag)
		require.NoError(t, err)
		assert.Empty(t, m.UUID)
	})
	t.Run("learned alias wins over the name match", func(t *testing.T) {
		_, err := pool.Exec(ctx, `INSERT INTO document_party_alias (party_kind, alias_key, party_uuid)
			VALUES ('vendor', $1, $2::uuid)`, "acme"+tag+" supply", dormant)
		require.NoError(t, err)
		m, err := r.ResolveVendor(ctx, "ACME"+tag+" Supply")
		require.NoError(t, err)
		assert.Equal(t, dormant, m.UUID)
		assert.Equal(t, docextract.SourceLearned, m.Source)
		// A customer-kind alias with the same key must not leak into vendors.
		_, err = pool.Exec(ctx, `INSERT INTO document_party_alias (party_kind, alias_key, party_uuid)
			VALUES ('customer', $1, $2::uuid)`, "zzcust"+tag, acme)
		require.NoError(t, err)
		m, err = r.ResolveVendor(ctx, "zzcust"+tag)
		require.NoError(t, err)
		assert.Empty(t, m.UUID)
	})
	t.Run("blank and wildcard text resolve nothing", func(t *testing.T) {
		for _, name := range []string{"", "   ", "%", "_"} {
			m, err := r.ResolveVendor(ctx, name)
			require.NoError(t, err)
			assert.Empty(t, m.UUID, "%q", name)
		}
	})
}

func TestResolve_VendorDocsUseVendorAndLinkedPO(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	tag := uniq()
	short := tag[len(tag)-9:]
	v := seedVendor(t, pool, "Linkco"+tag, "ACT_")
	_, poID := seedPurchaseOrder(t, pool, v, "PORD-"+short, "", "RCVD", 10000)
	seedPOLine(t, pool, poID, 1, "Slab", 10, 10, 1000)
	r := NewResolver(pool, ResolveOptions{})
	res := docextract.Result{DocType: docextract.DocTypeVendorBill, Header: docextract.Header{
		VendorName: docextract.Field{Value: "Linkco" + tag},
		PONumber:   docextract.Field{Value: "PO " + short},
	}}

	got, err := r.Resolve(ctx, res)
	require.NoError(t, err)
	assert.NotEmpty(t, got.Vendor.UUID)
	assert.Empty(t, got.Customer.UUID)
	require.NotNil(t, got.LinkedPO)
	assert.True(t, got.LinkedPO.Billable)

	res.DocType = docextract.DocTypePurchaseOrder
	got, err = r.Resolve(ctx, res)
	require.NoError(t, err)
	assert.NotEmpty(t, got.Vendor.UUID)
	assert.Nil(t, got.LinkedPO, "only a vendor bill links a purchase order")
}

func TestResolveDoc_VendorDocsStoreEmptyListsAsArrays(t *testing.T) {
	pool := testPool(t)
	for _, dt := range []docextract.DocType{docextract.DocTypePurchaseOrder, docextract.DocTypeVendorBill} {
		ex := &Extraction{ID: "00000000-0000-4000-8000-000000000001", DocType: string(dt)}
		doc, err := ResolveDoc(context.Background(), pool, ex, "sha", docextract.Result{DocType: dt}, nil)
		require.NoError(t, err)
		b, err := json.Marshal(doc)
		require.NoError(t, err)
		for _, want := range []string{`"lines":[]`, `"duplicates":[]`, `"pages":[]`} {
			assert.Contains(t, string(b), want, string(dt))
		}
	}
}
