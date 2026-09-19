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

// TestRegistry_RecordSpecComplete guards against the exact bug this test was
// added for: a registry entry with every other field set but a zero-value
// Record (RecordSpec{}), which compiles fine (RecordSpec is just strings)
// but makes every SQL statement engine.go builds from it syntactically
// invalid -- e.g. `SELECT , ,  FROM  WHERE  = $1` -- so every Get/Approve
// call for that module 500s at runtime. go build/vet/test all stay green
// because nothing here is a compile-time property; this loop is what
// catches it instead.
func TestRegistry_RecordSpecComplete(t *testing.T) {
	for key, cfg := range registry {
		t.Run(key, func(t *testing.T) {
			r := cfg.Record
			fields := map[string]string{
				"Table": r.Table, "HistoryTable": r.HistoryTable,
				"IDColumn": r.IDColumn, "UUIDColumn": r.UUIDColumn, "StatusColumn": r.StatusColumn,
				"ApprovalStatusColumn": r.ApprovalStatusColumn, "ApprovedByColumn": r.ApprovedByColumn,
				"UpdatedAtColumn": r.UpdatedAtColumn, "UpdatedByColumn": r.UpdatedByColumn,
				"RecordVersionColumn": r.RecordVersionColumn, "DeletedAtColumn": r.DeletedAtColumn,
				"CreatedAtColumn": r.CreatedAtColumn,
			}
			for name, val := range fields {
				if val == "" {
					t.Errorf("Record.%s is empty for %q -- every registered module's Record must be fully populated", name, key)
				}
			}
		})
	}
}

// TestRegistry_ApprovalNotificationScope guards the deliberate scope line
// notify.go's Notify* helpers rely on: DisplayName == "" is what makes them
// a no-op, so engine.Approve stays behavior-identical for every module not
// yet wired for approval notifications. Only the eleven engine-based
// in-scope modules below may set DisplayName (and, alongside it,
// Resource/OwnerColumn/NumberColumn) -- every other entry must stay at the
// zero value. If this test ever needs updating because a new module was
// deliberately wired for approval notifications, that's fine; it's here so
// that doesn't happen by accident.
func TestRegistry_ApprovalNotificationScope(t *testing.T) {
	inScope := map[string]bool{
		"invoice": true, "payment": true, "credit_memo": true, "refund": true,
		"purchase_order": true, "requisition": true, "vendor_bill": true,
		"vendor_payment": true, "expense": true, "installation": true,
		"vendor_credit": true,
	}
	for key, cfg := range registry {
		t.Run(key, func(t *testing.T) {
			if inScope[key] {
				if cfg.DisplayName == "" {
					t.Errorf("%q is approval-notification in-scope but DisplayName is empty", key)
				}
				if cfg.Resource == "" {
					t.Errorf("%q is approval-notification in-scope but Resource is empty", key)
				}
				if cfg.Record.OwnerColumn == "" {
					t.Errorf("%q is approval-notification in-scope but Record.OwnerColumn is empty", key)
				}
				if cfg.Record.NumberColumn == "" {
					t.Errorf("%q is approval-notification in-scope but Record.NumberColumn is empty", key)
				}
				return
			}
			if cfg.DisplayName != "" {
				t.Errorf("%q is not approval-notification in-scope but DisplayName = %q, want empty", key, cfg.DisplayName)
			}
			if cfg.Resource != "" {
				t.Errorf("%q is not approval-notification in-scope but Resource = %q, want empty", key, cfg.Resource)
			}
			if cfg.Record.OwnerColumn != "" {
				t.Errorf("%q is not approval-notification in-scope but Record.OwnerColumn = %q, want empty", key, cfg.Record.OwnerColumn)
			}
			if cfg.Record.NumberColumn != "" {
				t.Errorf("%q is not approval-notification in-scope but Record.NumberColumn = %q, want empty", key, cfg.Record.NumberColumn)
			}
		})
	}
}

// TestKeys verifies Keys() enumerates every registered module -- the KPI
// strip dashboard widget's "Needs Approval" aggregate (controllers) relies
// on this to iterate the registry from outside the package, since `registry`
// itself is unexported.
func TestKeys(t *testing.T) {
	keys := Keys()
	if len(keys) != len(registry) {
		t.Fatalf("len(Keys()) = %d, want %d (len(registry))", len(keys), len(registry))
	}
	seen := make(map[string]bool, len(keys))
	for _, k := range keys {
		if _, ok := registry[k]; !ok {
			t.Errorf("Keys() returned %q, which is not a registry key", k)
		}
		if seen[k] {
			t.Errorf("Keys() returned %q more than once", k)
		}
		seen[k] = true
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

// TestRegistry_RejectModes pins how every module's gate answers a Reject:
//   - the eight modules that gate on Pending Approval (Draft sits right before
//     it) send the record back to Draft;
//   - the four whose gate is their very first status (Credit Memo and Vendor
//     Credit gate on Draft, Payment and Refund on Pending -- nothing earlier
//     to return to) keep their status and flag the approval rejected in place;
//   - Expense (its own dedicated Reject -> RJCT) and Fabrication Job (mid-
//     production gates whose "reject" means rework) take no part.
//
// A module added to the registry must be classified here on purpose rather
// than silently getting the wrong (or no) Reject behavior.
func TestRegistry_RejectModes(t *testing.T) {
	want := map[string]RejectMode{
		"estimate": RejectToStatus, "quote": RejectToStatus, "sales_order": RejectToStatus,
		"invoice": RejectToStatus, "purchase_order": RejectToStatus, "requisition": RejectToStatus,
		"vendor_bill": RejectToStatus, "vendor_payment": RejectToStatus,
		"credit_memo": RejectInPlace, "vendor_credit": RejectInPlace,
		"payment": RejectInPlace, "refund": RejectInPlace,
		"expense": RejectUnsupported, "installation": RejectUnsupported,
	}
	for key := range registry {
		if _, ok := want[key]; !ok {
			t.Errorf("registry module %q has no expected Reject mode -- classify it in this test", key)
		}
	}
	for key, mode := range want {
		t.Run(key, func(t *testing.T) {
			cfg, ok := ForWorkflowKey(key)
			if !ok {
				t.Fatalf("%q is not registered", key)
			}
			for _, g := range cfg.Gates {
				if g.Reject != mode {
					t.Errorf("gate %s: Reject = %v, want %v", g.StatusCode, g.Reject, mode)
				}
				switch mode {
				case RejectToStatus:
					if g.RejectStatusCode != "DRFT" {
						t.Errorf("gate %s: RejectStatusCode = %q, want DRFT", g.StatusCode, g.RejectStatusCode)
					}
				default:
					if g.RejectStatusCode != "" {
						t.Errorf("gate %s: RejectStatusCode = %q, want empty for this mode", g.StatusCode, g.RejectStatusCode)
					}
				}
			}
		})
	}
}
