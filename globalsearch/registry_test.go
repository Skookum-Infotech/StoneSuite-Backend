package globalsearch

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRegistry_ExpectedKeys guards against a module silently missing
// registration -- the same clone-discipline trap CLAUDE.md warns about for
// approvalchain's registry. Adding a new searchable module must update this
// list, which makes a forgotten registration fail loudly instead of just
// shipping with no global-search coverage.
func TestRegistry_ExpectedKeys(t *testing.T) {
	want := []string{
		"customer", "lead", "prospect",
		"quote", "estimate", "sales_order", "invoice", "payment", "credit_memo", "refund",
		"vendor", "requisition", "purchase_order", "item_receipt", "vendor_bill", "vendor_payment", "vendor_credit", "expense",
		"inventory_item", "inventory_unit", "inventory_adjustment", "inventory_transfer", "inventory_count",
		"chart_of_account", "cash_transfer", "fabrication_job",
	}

	all := All()
	assert.Len(t, all, len(want), "registry size drifted from the expected module list")

	got := make(map[string]bool, len(all))
	for _, p := range all {
		got[p.Key] = true
	}
	for _, key := range want {
		assert.True(t, got[key], "expected provider %q to be registered", key)
	}
}

// TestRegistry_NoEmptyFields catches a copy-paste registration mistake (e.g. a
// forgotten Resource or Search func) across the ~26 near-identical entries.
func TestRegistry_NoEmptyFields(t *testing.T) {
	for _, p := range All() {
		t.Run(p.Key, func(t *testing.T) {
			assert.NotEmpty(t, p.Key)
			assert.NotEmpty(t, p.Resource)
			assert.NotNil(t, p.Search)
		})
	}
}
