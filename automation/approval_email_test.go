package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

func TestRenderApprovalDigest(t *testing.T) {
	old := config.AppConfig.FrontendURL
	config.AppConfig.FrontendURL = "https://app.example.test"
	t.Cleanup(func() { config.AppConfig.FrontendURL = old })
	today := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		items        []approvalLine
		wantLabels   []string
		wantValues   []string
		wantParas    int
		wantReason   string
		wantActionTo string
	}{
		{"singular", []approvalLine{{DisplayName: "Vendor Bill", Number: "VB-1", Route: "/purchases/vendor_bill/a", DaysWaiting: 1}},
			[]string{"Vendor Bill VB-1"}, []string{"waiting 1 day"}, 1, approvalEmailReason, "https://app.example.test/purchases/vendor_bill/a"},
		{"plural ordered oldest first", []approvalLine{
			{DisplayName: "Invoice", Number: "I-2", Route: "/sales/invoice/b", DaysWaiting: 2},
			{DisplayName: "Vendor Bill", Number: "VB-9", Route: "/purchases/vendor_bill/c", DaysWaiting: 6}},
			[]string{"Vendor Bill VB-9", "Invoice I-2"}, []string{"waiting 6 days", "waiting 2 days"}, 1, approvalEmailReason, "https://app.example.test/purchases/vendor_bill/c"},
		{"fallback adds paragraph", []approvalLine{{DisplayName: "Invoice", Number: "I-3", Route: "/sales/invoice/d", DaysWaiting: 4, Fallback: true}},
			[]string{"Invoice I-3"}, []string{"waiting 4 days"}, 2, approvalEmailReasonFallback, "https://app.example.test/sales/invoice/d"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := renderApprovalDigest("Acme", tc.items, today)
			require.Len(t, e.Details, len(tc.wantLabels))
			for i, d := range e.Details {
				assert.Equal(t, tc.wantLabels[i], d.Label)
				assert.Equal(t, tc.wantValues[i], d.Value)
			}
			assert.Len(t, e.Paragraphs, tc.wantParas)
			assert.Equal(t, tc.wantReason, e.Reason)
			assert.Equal(t, tc.wantActionTo, e.ActionURL)
			assert.NotContains(t, e.ActionURL, "public")
			assert.NotContains(t, e.ActionURL, "portal")
		})
	}
	t.Run("does not mutate input order", func(t *testing.T) {
		in := []approvalLine{{Number: "a", DaysWaiting: 1}, {Number: "b", DaysWaiting: 5}}
		renderApprovalDigest("", in, today)
		assert.Equal(t, "a", in[0].Number)
	})
}
