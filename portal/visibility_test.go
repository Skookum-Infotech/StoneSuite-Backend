package portal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The visibility table is the entire definition of what a customer may see, so
// it is asserted directly rather than through a query. A change here is a
// change to the product's disclosure policy and should have to be deliberate.
func TestVisibleHidesUnfinishedDocuments(t *testing.T) {
	tests := []struct {
		name     string
		module   Module
		typeCode string
		shown    []string
		hidden   []string
	}{
		{
			name:     "sales order hides drafts and pending approval",
			module:   ModuleSalesOrder,
			typeCode: "SORD",
			shown:    []string{"APPV", "OPEN", "PART", "FILL", "CANC"},
			hidden:   []string{"DRFT", "PAPV"},
		},
		{
			// APPV is hidden here but shown for sales orders: an approved
			// invoice has not been issued to the customer until it is SENT.
			name:     "invoice hides drafts, pending approval and un-sent approvals",
			module:   ModuleInvoice,
			typeCode: "INVC",
			shown:    []string{"SENT", "PART", "PAID", "ODUE", "VOID"},
			hidden:   []string{"DRFT", "PAPV", "APPV"},
		},
		{
			name:     "payment hides pending",
			module:   ModulePayment,
			typeCode: "PYMT",
			shown:    []string{"APPV", "DEPO", "VOID"},
			hidden:   []string{"PEND"},
		},
		{
			name:     "refund hides pending",
			module:   ModuleRefund,
			typeCode: "RFND",
			shown:    []string{"APPV", "SENT", "VOID"},
			hidden:   []string{"PEND"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vis, ok := Visible(tt.module)
			require.True(t, ok, "module %s must have a visibility rule", tt.module)
			assert.Equal(t, tt.typeCode, vis.RecordTypeCode)
			assert.ElementsMatch(t, tt.shown, vis.StatusCodes)
			for _, h := range tt.hidden {
				assert.NotContains(t, vis.StatusCodes, h,
					"%s must not be visible to a customer", h)
			}
		})
	}
}

// An unknown module must yield "show nothing", never "show everything". A
// caller that dropped the status filter on a miss would expose every document.
func TestVisibleFailsClosedOnUnknownModule(t *testing.T) {
	vis, ok := Visible(Module("workorder"))
	assert.False(t, ok)
	assert.Empty(t, vis.StatusCodes)
	assert.Empty(t, vis.RecordTypeCode)
}

func TestValidModule(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"sales_order", true},
		{"invoice", true},
		{"payment", true},
		{"refund", true},
		{"quote", false},       // real module, but not portal-visible
		{"estimate", false},    // ditto
		{"credit_memo", false}, // ditto
		{"", false},
		{"invoice; DROP TABLE", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			_, ok := ValidModule(tt.in)
			assert.Equal(t, tt.want, ok)
		})
	}
}

// Every declared module must be resolvable, so Modules() and the table cannot
// drift apart.
func TestModulesAllResolve(t *testing.T) {
	mods := Modules()
	require.Len(t, mods, 4)
	for _, m := range mods {
		vis, ok := Visible(m)
		require.True(t, ok)
		assert.NotEmpty(t, vis.StatusCodes, "%s must list at least one visible status", m)
		assert.NotEmpty(t, vis.RecordTypeCode)
	}
}

// No portal-visible status list may contain a draft or pending-approval state,
// whatever else changes. This is the invariant the individual cases above are
// instances of: internal working state never reaches a customer.
func TestNoModuleExposesWorkingState(t *testing.T) {
	forbidden := []string{"DRFT", "PAPV", "PEND"}
	for _, m := range Modules() {
		vis, _ := Visible(m)
		for _, code := range vis.StatusCodes {
			for _, f := range forbidden {
				assert.NotEqual(t, f, code,
					"module %s exposes working state %s to customers", m, code)
			}
		}
	}
}

// The URL segment and the stored module value differ on purpose. These assert
// the mapping both ways, because a mismatch would put the message endpoints at
// a different spelling from the documents they hang off — the exact trap this
// mapping exists to remove.
func TestModuleForURLMatchesDocumentRoutes(t *testing.T) {
	tests := []struct {
		slug string
		want Module
	}{
		{"sales-orders", ModuleSalesOrder},
		{"invoices", ModuleInvoice},
		{"payments", ModulePayment},
		{"refunds", ModuleRefund},
	}
	for _, tt := range tests {
		t.Run(tt.slug, func(t *testing.T) {
			got, ok := ModuleForURL(tt.slug)
			require.True(t, ok, "%q must resolve; it is the path the documents are served under", tt.slug)
			assert.Equal(t, tt.want, got)
		})
	}
}

// Anything else must fail closed — including the stored module values, which
// are deliberately NOT valid URL segments.
func TestModuleForURLFailsClosed(t *testing.T) {
	for _, bad := range []string{
		"invoice", "sales_order", "payment", "refund", // stored values, not URL slugs
		"quotes", "estimates", "credit-memos", "users", "", "..", "*",
	} {
		_, ok := ModuleForURL(bad)
		assert.False(t, ok, "%q must not resolve to a module", bad)
	}
}

// Every module must be reachable by URL, or its message endpoint is unroutable.
func TestEveryModuleHasAURLSlug(t *testing.T) {
	for _, m := range Modules() {
		slug, ok := URLSlug(m)
		require.True(t, ok, "module %q has no URL segment", m)
		back, ok := ModuleForURL(slug)
		require.True(t, ok)
		assert.Equal(t, m, back, "round-trip through the URL slug must be lossless")
	}
}
