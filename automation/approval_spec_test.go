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
	tests := []struct {
		name    string
		mutate  func(*approvalSpec)
		wantMsg string
	}{
		{"valid", func(*approvalSpec) {}, ""},
		{"approver table", func(s *approvalSpec) { s.Cfg.ApproverTable = "x; DROP TABLE y" }, "approver table"},
		{"approval table", func(s *approvalSpec) { s.Cfg.ApprovalTable = "" }, "approval table"},
		{"table", func(s *approvalSpec) { s.Cfg.Record.Table = "A" }, "invalid table"},
		{"history table", func(s *approvalSpec) { s.Cfg.Record.HistoryTable = "h h" }, "history table"},
		{"id column", func(s *approvalSpec) { s.Cfg.Record.IDColumn = "1id" }, "id column"},
		{"uuid column", func(s *approvalSpec) { s.Cfg.Record.UUIDColumn = "u;" }, "uuid column"},
		{"status column", func(s *approvalSpec) { s.Cfg.Record.StatusColumn = "s)" }, "status column"},
		{"approval status column", func(s *approvalSpec) { s.Cfg.Record.ApprovalStatusColumn = "" }, "approval status column"},
		{"deleted column", func(s *approvalSpec) { s.Cfg.Record.DeletedAtColumn = "d d" }, "deleted column"},
		{"created column", func(s *approvalSpec) { s.Cfg.Record.CreatedAtColumn = "" }, "created column"},
		{"owner column", func(s *approvalSpec) { s.Cfg.Record.OwnerColumn = "" }, "owner column"},
		{"number column", func(s *approvalSpec) { s.Cfg.Record.NumberColumn = "n'" }, "number column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := specs[0]
			tt.mutate(&s)
			err := s.ident()
			if tt.wantMsg == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantMsg)
		})
	}
}
