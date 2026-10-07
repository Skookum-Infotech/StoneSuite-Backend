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

const fixtureDir = "../testdata/docextract/sales_order"

type fixtureExpected struct {
	Header   map[string]string `json:"header"`
	Revision string            `json:"revision"`
	QuoteRef string            `json:"quote_ref"`
	Lines    []struct {
		SKU         string `json:"sku"`
		Description string `json:"description"`
		Qty         string `json:"qty"`
		UoM         string `json:"uom"`
		UnitPrice   string `json:"unit_price"`
		Amount      string `json:"amount"`
	} `json:"lines"`
}

func headerMap(h Header) map[string]string {
	return map[string]string{
		"po_number": h.PONumber.Value, "order_date": h.OrderDate.Value, "delivery_date": h.DeliveryDate.Value,
		"customer_name": h.CustomerName.Value, "payment_terms": h.PaymentTerms.Value,
		"subtotal": h.Subtotal.Value, "tax": h.Tax.Value, "shipping": h.Shipping.Value,
		"discount": h.Discount.Value, "total": h.Total.Value, "currency": h.Currency.Value,
		"bill_to": h.BillTo.Value, "ship_to": h.ShipTo.Value, "notes": h.Notes.Value,
	}
}

func runFixture(t *testing.T, base string) (Result, fixtureExpected) {
	t.Helper()
	raw, err := os.ReadFile(base + ".expected.json")
	require.NoError(t, err)
	var exp fixtureExpected
	require.NoError(t, json.Unmarshal(raw, &exp))
	file := base + ".pdf"
	if _, err := os.Stat(file); err != nil {
		file = base + ".docx"
	}
	b, err := os.ReadFile(file)
	require.NoError(t, err)
	res, method, err := Extract(context.Background(), b, DocTypeSalesOrder, Options{ReconcileFn: exactReconcile})
	require.NoError(t, err)
	assert.Equal(t, MethodParser, method)
	return res, exp
}

func TestFixtures_GoldenSet(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixtureDir, "*.expected.json"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(files), 10)
	for _, ef := range files {
		base := strings.TrimSuffix(ef, ".expected.json")
		t.Run(filepath.Base(base), func(t *testing.T) {
			res, exp := runFixture(t, base)
			got := headerMap(res.Header)
			for k, want := range exp.Header {
				assert.Equal(t, want, got[k], "header %s", k)
			}
			require.Len(t, res.Lines, len(exp.Lines))
			for i, el := range exp.Lines {
				l := res.Lines[i]
				assert.Equal(t, el.SKU, l.SKU.Value, "line %d sku", i+1)
				assert.Equal(t, el.Description, l.Description.Value, "line %d desc", i+1)
				assert.Equal(t, el.Qty, l.Qty.Value, "line %d qty", i+1)
				assert.Equal(t, el.UoM, l.UoM.Value, "line %d uom", i+1)
				assert.Equal(t, el.UnitPrice, l.UnitPrice.Value, "line %d price", i+1)
				assert.Equal(t, el.Amount, l.Amount.Value, "line %d amount", i+1)
			}
			if exp.Revision != "" {
				require.NotNil(t, res.Revision)
				assert.Equal(t, exp.Revision, res.Revision.Label+"|"+res.Revision.ReferencedNumber)
			} else {
				assert.Nil(t, res.Revision)
			}
			assert.Equal(t, exp.QuoteRef, res.QuoteRef)
			assert.Empty(t, res.Unresolved)
			assert.NotContains(t, res.Warnings, WarnMultipleDocuments)
		})
	}
}

func TestFixtures_Specifics(t *testing.T) {
	load := func(name string) Result {
		res, _ := runFixture(t, filepath.Join(fixtureDir, name))
		return res
	}
	t.Run("two pages keep original page numbers", func(t *testing.T) {
		res := load("03_two_pages_repeated_header")
		assert.Equal(t, 1, res.Lines[0].Description.Page)
		assert.Equal(t, 2, res.Lines[5].Description.Page)
	})
	t.Run("add-ons link to their parent", func(t *testing.T) {
		res := load("04_addon_lines")
		var kinds []LineKind
		var parents []int
		for _, l := range res.Lines {
			kinds = append(kinds, l.Kind)
			parents = append(parents, l.ParentLine)
		}
		assert.Equal(t, []LineKind{KindProduct, KindAddon, KindAddon, KindProduct, KindAddon}, kinds)
		assert.Equal(t, []int{0, 1, 1, 0, 4}, parents)
	})
	t.Run("totals mismatch is flagged not fixed", func(t *testing.T) {
		res := load("08_totals_mismatch")
		require.Len(t, res.Checks, 1)
		assert.False(t, res.Checks[0].Passed)
		assert.Equal(t, Cents(100000), res.Checks[0].Expected)
		assert.Equal(t, Cents(99000), res.Checks[0].Actual)
		assert.Equal(t, "1000.00", res.Header.Subtotal.Value)
	})
	t.Run("clean totals pass", func(t *testing.T) {
		res := load("01_simple_table")
		require.Len(t, res.Checks, 1)
		assert.True(t, res.Checks[0].Passed)
	})
	t.Run("freight and discount feed header only", func(t *testing.T) {
		res := load("05_freight_and_discount")
		assert.Len(t, res.Lines, 2)
		assert.Equal(t, "150.00", res.Header.Shipping.Value)
		assert.Equal(t, "175.00", res.Header.Discount.Value)
	})
	t.Run("provenance snippets stay short", func(t *testing.T) {
		res := load("02_wrapped_descriptions")
		for _, l := range res.Lines {
			assert.LessOrEqual(t, len(l.Description.Snippet), MaxSnippetLen)
		}
		assert.Equal(t, 1, res.Header.CustomerName.Page)
	})
}
