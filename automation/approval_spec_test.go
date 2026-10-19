package automation

import (
	"testing"

	"stonesuite-backend/approvalchain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApprovalSpecs(t *testing.T) {
	specs := approvalSpecs()
	counts := map[string][]string{}
	for _, s := range specs {
		assert.NotEmpty(t, s.DisplayName, s.Workflow)
		assert.NotEqual(t, approvalchain.RejectInPlace, s.Gate.Reject, s.Workflow)
		assert.NoError(t, s.ident(), s.Workflow)
		counts[s.Workflow] = append(counts[s.Workflow], s.Gate.StatusCode)
	}

	tests := []struct {
		name      string
		workflow  string
		wantGates []string
	}{
		{"installation two gates", "installation", []string{"TMPL", "QCPD"}},
		{"purchase_order", "purchase_order", nil},
		{"vendor_bill", "vendor_bill", nil},
		{"invoice", "invoice", nil},
		{"expense", "expense", nil},
		{"bin_transfer", "bin_transfer", nil},
		{"quote excluded", "quote", []string{}},
		{"estimate excluded", "estimate", []string{}},
		{"sales_order excluded", "sales_order", []string{}},
		{"payment excluded", "payment", []string{}},
		{"refund excluded", "refund", []string{}},
		{"credit_memo excluded", "credit_memo", []string{}},
		{"vendor_credit excluded", "vendor_credit", []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := counts[tt.workflow]
			switch {
			case tt.wantGates == nil:
				assert.Len(t, got, 1)
			case len(tt.wantGates) == 0:
				assert.Empty(t, got)
			default:
				assert.Equal(t, tt.wantGates, got)
			}
		})
	}
}

func TestApprovalSpecIdent(t *testing.T) {
	specs := approvalSpecs()
	require.NotEmpty(t, specs)
	bad := specs[0]
	bad.Cfg.ApproverTable = "x; DROP TABLE y"
	assert.Error(t, bad.ident())
}
