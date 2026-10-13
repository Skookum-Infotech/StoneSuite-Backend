//go:build dbtest

package docextractjob

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/docextract"
)

// storeReadyExtraction stores res as the result of a ready extraction for ownerA.
func storeReadyExtraction(t *testing.T, s *Store, res ResultDoc) *Extraction {
	t.Helper()
	ctx := context.Background()
	e := newExtraction(t, s, ownerA)
	_, _, err := s.MarkQueued(ctx, e.ID, ownerA, "", "job")
	require.NoError(t, err)
	require.NoError(t, s.MarkRunning(ctx, e.ID))
	require.NoError(t, s.SaveResult(ctx, e.ID, SaveResultParams{SHA256: "s", Method: "parser", Result: res}))
	return e
}

func TestRecordCompletion_LearnsAliasesAndFeedback(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	tag := uniq()
	cust := seedCustomer(t, pool, "Realco"+tag, "CACT")
	item := seedItem(t, pool, "R-1"+tag, "Real slab"+tag, "SQFT", 5000, true)
	rec := "44444444-4444-4444-8444-444444444444"

	res := feedbackResult("", "", "")
	res.Extracted.Header.CustomerName = docextract.Field{Value: "Real Co. Ltd"}
	res.Extracted.Lines[0].SKU = docextract.Field{Value: "VND-9"}
	res.Extracted.Lines[0].Description = docextract.Field{Value: "Vendor slab"}
	e := storeReadyExtraction(t, s, res)

	saved := SavedValues{
		CustomerUUID: cust, PONumber: "PO-2", OrderDate: "2026-01-02",
		Lines: []SavedLine{{DocSKU: "VND-9", DocDescription: "Vendor slab", ItemUUID: item}},
	}
	require.NoError(t, RecordCompletion(ctx, pool, e.ID, ownerA, rec, saved))

	var partyUUID string
	var hits int
	require.NoError(t, pool.QueryRow(ctx, `SELECT party_uuid::text, hits FROM document_party_alias WHERE party_kind='customer' AND alias_key='real co. ltd'`).Scan(&partyUUID, &hits))
	assert.Equal(t, cust, partyUUID)
	assert.Equal(t, 1, hits)

	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM document_item_alias WHERE party_uuid=$1::uuid AND item_uuid=$2::uuid AND alias_key IN ('sku:vnd9','desc:vendor slab')`, cust, item).Scan(&n))
	assert.Equal(t, 2, n)

	rows, err := pool.Query(ctx, `SELECT field FROM document_extraction_feedback WHERE extraction_id=$1::uuid AND layout_fingerprint='fp' ORDER BY field`, e.ID)
	require.NoError(t, err)
	var fields []string
	for rows.Next() {
		var f string
		require.NoError(t, rows.Scan(&f))
		fields = append(fields, f)
	}
	rows.Close()
	assert.Equal(t, []string{FieldCustomer, FieldLineItem, FieldPONumber}, fields)

	// Learning again bumps hits rather than duplicating.
	require.NoError(t, RecordCompletion(ctx, pool, e.ID, ownerA, rec, saved))
	require.NoError(t, pool.QueryRow(ctx, `SELECT hits FROM document_party_alias WHERE alias_key='real co. ltd'`).Scan(&hits))
	assert.Equal(t, 2, hits)
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM document_item_alias`).Scan(&n))
	assert.Equal(t, 2, n)

	// The learned alias now resolves the same document text.
	m, err := NewResolver(pool, ResolveOptions{}).ResolveCustomer(ctx, "REAL CO. LTD")
	require.NoError(t, err)
	assert.Equal(t, cust, m.UUID)
	assert.Equal(t, docextract.SourceLearned, m.Source)
}

func TestRecordCompletion_OwnerAndForgedUUIDs(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	res := feedbackResult("", "", "")
	e := storeReadyExtraction(t, s, res)
	rec := "44444444-4444-4444-8444-444444444444"

	err := RecordCompletion(ctx, pool, e.ID, ownerB, rec, SavedValues{})
	assert.ErrorIs(t, err, ErrNotFound, "another owner's extraction cannot teach")

	// A customer / item uuid that does not exist learns nothing (no alias row).
	forged := SavedValues{CustomerUUID: custB, Lines: []SavedLine{{DocSKU: "AB-1", ItemUUID: itemB}}}
	require.NoError(t, RecordCompletion(ctx, pool, e.ID, ownerA, rec, forged))
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT COUNT(*) FROM document_party_alias) + (SELECT COUNT(*) FROM document_item_alias)`).Scan(&n))
	assert.Equal(t, 0, n)

	// A malformed customer id is tolerated and ignored, never a 500.
	require.NoError(t, RecordCompletion(ctx, pool, e.ID, ownerA, rec, SavedValues{CustomerUUID: "x'; --"}))
	assert.ErrorIs(t, RecordCompletion(ctx, pool, "bad", ownerA, rec, SavedValues{}), ErrNotFound)
}

func TestRecordCompletion_LearnsVendorAliasAndItemsKeyedByVendor(t *testing.T) {
	pool := testPool(t)
	s := NewStore(pool)
	ctx := context.Background()
	tag := uniq()
	vendor := seedVendor(t, pool, "Realv"+tag, "ACT_")
	cust := seedCustomer(t, pool, "Notv"+tag, "CACT")
	item := seedItem(t, pool, "RV-1"+tag, "Real slab v"+tag, "SQFT", 5000, true)
	rec := "44444444-4444-4444-8444-444444444444"

	res := feedbackResult("", "", "")
	res.Extracted.DocType = docextract.DocTypeVendorBill
	res.Extracted.Header.VendorName = docextract.Field{Value: "Real V Ltd"}
	res.Extracted.Header.InvoiceNumber = docextract.Field{Value: "INV-1"}
	res.Extracted.Lines[0].SKU = docextract.Field{Value: "VND-9"}
	e := storeReadyExtraction(t, s, res)

	saved := SavedValues{
		VendorUUID: vendor, CustomerUUID: cust, InvoiceNumber: "INV-2", PONumber: "PO-1",
		Lines: []SavedLine{{DocSKU: "VND-9", DocDescription: "Slab one", ItemUUID: item}},
	}
	require.NoError(t, RecordCompletion(ctx, pool, e.ID, ownerA, rec, saved))

	var partyUUID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT party_uuid::text FROM document_party_alias WHERE party_kind='vendor' AND alias_key='real v ltd'`).Scan(&partyUUID))
	assert.Equal(t, vendor, partyUUID)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM document_party_alias WHERE party_kind='customer'`).Scan(&n))
	assert.Equal(t, 0, n, "a vendor document never teaches a customer alias")
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM document_item_alias WHERE party_uuid=$1::uuid AND item_uuid=$2::uuid`, vendor, item).Scan(&n))
	assert.Equal(t, 2, n, "item aliases are keyed by the vendor uuid")

	rows, err := pool.Query(ctx, `SELECT field FROM document_extraction_feedback WHERE extraction_id=$1::uuid ORDER BY field`, e.ID)
	require.NoError(t, err)
	var fields []string
	for rows.Next() {
		var f string
		require.NoError(t, rows.Scan(&f))
		fields = append(fields, f)
	}
	rows.Close()
	assert.Equal(t, []string{FieldInvoiceNumber, FieldLineItem, FieldVendor}, fields)

	// The learned alias resolves the same vendor text next time.
	m, err := NewResolver(pool, ResolveOptions{}).ResolveVendor(ctx, "REAL V LTD")
	require.NoError(t, err)
	assert.Equal(t, vendor, m.UUID)
	assert.Equal(t, docextract.SourceLearned, m.Source)

	// A forged vendor uuid learns nothing.
	res2 := feedbackResult("", "", "")
	res2.Extracted.DocType = docextract.DocTypePurchaseOrder
	res2.Extracted.Header.VendorName = docextract.Field{Value: "Ghost Co"}
	e2 := storeReadyExtraction(t, s, res2)
	require.NoError(t, RecordCompletion(ctx, pool, e2.ID, ownerA, rec, SavedValues{VendorUUID: custB}))
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM document_party_alias WHERE alias_key='ghost co'`).Scan(&n))
	assert.Equal(t, 0, n)
}
