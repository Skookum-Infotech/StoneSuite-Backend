package approvalchain

import "testing"

func TestForWorkflowKey(t *testing.T) {
	tests := []struct {
		key       string
		wantOK    bool
		wantRTC   string
		wantGates []string
	}{
		{"estimate", true, "ESTM", []string{"PAPV"}},
		{"quote", true, "QUOT", []string{"PAPV"}},
		{"sales_order", true, "SORD", []string{"PAPV"}},
		{"purchase_order", true, "PORD", []string{"PAPV"}},
		{"requisition", true, "REQN", []string{"PAPV"}},
		{"vendor_bill", true, "VBIL", []string{"PAPV"}},
		{"vendor_payment", true, "VPAY", []string{"PAPV"}},
		{"expense", true, "EXPN", []string{"SUBM"}},
		{"installation", true, "FJOB", []string{"TMPL", "QCPD"}},
		{"lead", false, "", nil},
		{"prospect", false, "", nil},
		{"customer", false, "", nil},
		{"vendor", false, "", nil},
		{"invoice", false, "", nil},
		{"item_receipt", false, "", nil},
		{"vendor_credit", false, "", nil},
		{"payment", false, "", nil},
		{"credit_memo", false, "", nil},
		{"refund", false, "", nil},
		{"not_a_real_key", false, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			cfg, ok := ForWorkflowKey(tt.key)
			if ok != tt.wantOK {
				t.Fatalf("ForWorkflowKey(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if cfg.RecordTypeCode != tt.wantRTC {
				t.Errorf("RecordTypeCode = %q, want %q", cfg.RecordTypeCode, tt.wantRTC)
			}
			if cfg.ApproverTable == "" {
				t.Error("ApproverTable must not be empty")
			}
			if len(cfg.Gates) != len(tt.wantGates) {
				t.Fatalf("len(Gates) = %d, want %d", len(cfg.Gates), len(tt.wantGates))
			}
			for i, g := range cfg.Gates {
				if g.StatusCode != tt.wantGates[i] {
					t.Errorf("Gates[%d].StatusCode = %q, want %q", i, g.StatusCode, tt.wantGates[i])
				}
			}
		})
	}
}

func TestModuleConfig_HasGate(t *testing.T) {
	fjob, _ := ForWorkflowKey("installation")
	if !fjob.HasGate("TMPL") {
		t.Error("expected HasGate(TMPL) = true for installation")
	}
	if !fjob.HasGate("QCPD") {
		t.Error("expected HasGate(QCPD) = true for installation")
	}
	if fjob.HasGate("PAPV") {
		t.Error("expected HasGate(PAPV) = false for installation")
	}

	est, _ := ForWorkflowKey("estimate")
	if !est.HasGate("PAPV") {
		t.Error("expected HasGate(PAPV) = true for estimate")
	}
	if est.HasGate("SUBM") {
		t.Error("expected HasGate(SUBM) = false for estimate")
	}
}
