package docextract

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testdataRoot = "../testdata/docextract"

type vendorExpected struct {
	fixtureExpected
	Class    string   `json:"class"`
	Warnings []string `json:"warnings"`
}

func loadVendorFixture(t *testing.T, base string, dt DocType, llm LLM) (Result, vendorExpected) {
	t.Helper()
	raw, err := os.ReadFile(base + ".expected.json")
	require.NoError(t, err)
	var exp vendorExpected
	require.NoError(t, json.Unmarshal(raw, &exp))
	b, err := os.ReadFile(base + ".pdf")
	require.NoError(t, err)
	res, _, err := Extract(context.Background(), b, dt, Options{OwnCompanyNames: []string{ownName}, LLM: llm, ReconcileFn: exactReconcile})
	require.NoError(t, err)
	return res, exp
}

// vendorHeaderMap extends headerMap with the vendor-side keys.
func vendorHeaderMap(h Header) map[string]string {
	m := headerMap(h)
	m["vendor_name"], m["invoice_number"] = h.VendorName.Value, h.InvoiceNumber.Value
	m["invoice_date"], m["due_date"] = h.InvoiceDate.Value, h.DueDate.Value
	return m
}

func TestFixtures_VendorSideGoldenSet(t *testing.T) {
	for _, dt := range []DocType{DocTypePurchaseOrder, DocTypeVendorBill} {
		files, err := filepath.Glob(filepath.Join(testdataRoot, string(dt), "*.expected.json"))
		require.NoError(t, err)
		require.NotEmpty(t, files)
		for _, ef := range files {
			base := strings.TrimSuffix(ef, ".expected.json")
			t.Run(string(dt)+"/"+filepath.Base(base), func(t *testing.T) {
				llm := &fakeLLM{reply: `{"vendor_name":"Should Not Be Used"}`}
				res, exp := loadVendorFixture(t, base, dt, llm)
				got := vendorHeaderMap(res.Header)
				for k, want := range exp.Header {
					assert.Equal(t, want, got[k], "header %s", k)
				}
				if len(exp.Lines) > 0 {
					require.Len(t, res.Lines, len(exp.Lines))
					for i, el := range exp.Lines {
						assert.Equal(t, el.SKU, res.Lines[i].SKU.Value, "line %d sku", i+1)
						assert.Equal(t, el.Description, res.Lines[i].Description.Value, "line %d desc", i+1)
						assert.Equal(t, el.Qty, res.Lines[i].Qty.Value, "line %d qty", i+1)
						assert.Equal(t, el.Amount, res.Lines[i].Amount.Value, "line %d amount", i+1)
					}
				}
				assert.Equal(t, exp.Class, res.ClassifiedAs)
				for _, w := range exp.Warnings {
					assert.Contains(t, res.Warnings, w)
				}
				assert.NotContains(t, res.Warnings, WarnMultipleDocuments)
				assert.NotContains(t, res.Warnings, WarnCustomerPO)
				assert.False(t, res.Header.CustomerName.Found(), "vendor-side documents never name a customer")
				assert.NotNil(t, res.Lines)
			})
		}
	}
}

func TestFixtures_VendorSideSpecifics(t *testing.T) {
	dir := func(dt DocType, name string) string { return filepath.Join(testdataRoot, string(dt), name) }

	t.Run("order form as PO: no party, builder in notes, never the model", func(t *testing.T) {
		llm := &fakeLLM{reply: `{"vendor_name":"Lakeview Homes"}`}
		res, _ := loadVendorFixture(t, dir(DocTypePurchaseOrder, "03_builder_order_form"), DocTypePurchaseOrder, llm)
		assert.Equal(t, ClassOrderForm, res.ClassifiedAs)
		assert.False(t, res.Header.VendorName.Found())
		assert.False(t, res.Header.CustomerName.Found())
		assert.Contains(t, res.Header.Notes.Value, "dana.reyes@lakeviewhomes.com")
		assert.Contains(t, res.Warnings, WarnNoPrices)
		assert.Contains(t, res.Unresolved, KeyVendorName)
		assert.Equal(t, 0, llm.calls)
	})
	t.Run("fillable PO reads typed values", func(t *testing.T) {
		res, _ := loadVendorFixture(t, dir(DocTypePurchaseOrder, "04_fillable_po"), DocTypePurchaseOrder, nil)
		assert.Equal(t, "Summit Slab Co", res.Header.VendorName.Value)
		assert.Equal(t, "FP-7001", res.Header.PONumber.Value)
	})
	t.Run("vendor quote classifies as a quote and is accepted", func(t *testing.T) {
		res, _ := loadVendorFixture(t, dir(DocTypePurchaseOrder, "01_vendor_quote"), DocTypePurchaseOrder, nil)
		assert.NotContains(t, res.Warnings, WarnDocLooksLike+ClassQuote)
		assert.Empty(t, res.QuoteRef, "the quote number is the PO number, not a reference to another quote")
		assert.Equal(t, ConfCheck, res.Header.VendorName.Confidence)
	})
	t.Run("bill due date comes from the terms", func(t *testing.T) {
		res, _ := loadVendorFixture(t, dir(DocTypeVendorBill, "01_bill_po_ref_net30"), DocTypeVendorBill, nil)
		assert.Equal(t, SourceDefault, res.Header.DueDate.Source)
		assert.Equal(t, ConfCheck, res.Header.DueDate.Confidence)
		assert.Empty(t, res.Unresolved)
	})
	t.Run("multi-page bill: charges feed the header, deposit is never a line", func(t *testing.T) {
		res, _ := loadVendorFixture(t, dir(DocTypeVendorBill, "02_bill_multipage_freight_deposit"), DocTypeVendorBill, nil)
		assert.Equal(t, "204.00", res.Header.Shipping.Value)
		assert.Equal(t, 2, res.Lines[len(res.Lines)-1].Description.Page)
		for _, l := range res.Lines {
			assert.NotContains(t, strings.ToLower(l.Description.Value), "deposit")
		}
		assert.Equal(t, "5004.50", res.Header.Total.Value, "Balance Due does not replace the invoice total")
		assert.Nil(t, res.Revision)
	})
	t.Run("statement as a bill: mismatch and zero model calls", func(t *testing.T) {
		llm := &fakeLLM{reply: `{"vendor_name":"Granite Works LLC","invoice_number":"GW-20418"}`}
		res, _ := loadVendorFixture(t, dir(DocTypeVendorBill, "03_statement_mismatch"), DocTypeVendorBill, llm)
		assert.Contains(t, res.Warnings, WarnDocLooksLike+ClassStatement)
		assert.Equal(t, 0, llm.calls)
		assert.NotNil(t, res.Lines)
	})
}

// The sales-order path must not change when an own company name is supplied.
func TestFixtures_SalesOrderUnchangedWithOwnName(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.expected.json"))
	require.NoError(t, err)
	for _, ef := range files {
		base := strings.TrimSuffix(ef, ".expected.json")
		t.Run(filepath.Base(base), func(t *testing.T) {
			file := base + ".pdf"
			if _, err := os.Stat(file); err != nil {
				file = base + ".docx"
			}
			b, err := os.ReadFile(file)
			require.NoError(t, err)
			plain, _, err := Extract(context.Background(), b, DocTypeSalesOrder, Options{ReconcileFn: exactReconcile})
			require.NoError(t, err)
			withOwn, _, err := Extract(context.Background(), b, DocTypeSalesOrder, Options{ReconcileFn: exactReconcile, OwnCompanyNames: []string{ownName}})
			require.NoError(t, err)
			assert.Equal(t, headerMap(plain.Header), headerMap(withOwn.Header))
			assert.Equal(t, plain.Lines, withOwn.Lines)
			assert.Equal(t, plain.Warnings, withOwn.Warnings)
			assert.False(t, withOwn.Header.VendorName.Found(), "a sales order never reads the vendor-side fields")
		})
	}
}
