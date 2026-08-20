package approvalchain

import "testing"

func TestForWorkflowKey(t *testing.T) {
	tests := []struct {
		key         string
		wantOK      bool
		wantRTC     string
		wantGates   []string
		wantTargets []string
	}{
		{"estimate", true, "ESTM", []string{"PAPV"}, []string{"APPV"}},
		{"quote", true, "QUOT", []string{"PAPV"}, []string{"APPV"}},
		{"sales_order", true, "SORD", []string{"PAPV"}, []string{"APPV"}},
		{"purchase_order", true, "PORD", []string{"PAPV"}, []string{"APPV"}},
		{"requisition", true, "REQN", []string{"PAPV"}, []string{"APPV"}},
		{"vendor_bill", true, "VBIL", []string{"PAPV"}, []string{"APPV"}},
		{"vendor_payment", true, "VPAY", []string{"PAPV"}, []string{"APPV"}},
		{"expense", true, "EXPN", []string{"SUBM"}, []string{"APPV"}},
		{"installation", true, "FJOB", []string{"TMPL", "QCPD"}, []string{"TAPV", "QCPS"}},
		{"invoice", true, "INVC", []string{"PAPV"}, []string{"APPV"}},
		{"payment", true, "PYMT", []string{"PEND"}, []string{"APPV"}},
		{"credit_memo", true, "CRDT", []string{"DRFT"}, []string{"APPV"}},
		{"refund", true, "RFND", []string{"PEND"}, []string{"APPV"}},
		{"vendor_credit", true, "VCRD", []string{"DRFT"}, []string{"APPV"}},
		{"lead", false, "", nil, nil},
		{"prospect", false, "", nil, nil},
		{"customer", false, "", nil, nil},
		{"vendor", false, "", nil, nil},
		{"item_receipt", false, "", nil, nil},
		{"not_a_real_key", false, "", nil, nil},
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
			if cfg.ApprovalTable == "" {
				t.Error("ApprovalTable must not be empty")
			}
			if len(cfg.Gates) != len(tt.wantGates) {
				t.Fatalf("len(Gates) = %d, want %d", len(cfg.Gates), len(tt.wantGates))
			}
			for i, g := range cfg.Gates {
				if g.StatusCode != tt.wantGates[i] {
					t.Errorf("Gates[%d].StatusCode = %q, want %q", i, g.StatusCode, tt.wantGates[i])
				}
				if g.TargetStatusCode != tt.wantTargets[i] {
					t.Errorf("Gates[%d].TargetStatusCode = %q, want %q", i, g.TargetStatusCode, tt.wantTargets[i])
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
