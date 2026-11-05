package approvalinbox

import (
	"testing"

	"stonesuite-backend/approvalchain"
)

// A new approvalchain registry key without inbox metadata would silently never show up
// in anyone's inbox -- fail loudly instead.
func TestMetaCoversRegistry(t *testing.T) {
	for _, key := range approvalchain.Keys() {
		m, ok := MetaFor(key)
		if !ok {
			t.Errorf("approvalchain key %q has no approvalinbox metadata (add it to metas in meta.go)", key)
			continue
		}
		if m.Label == "" || m.Resource == "" || m.Domain == "" || m.RouteModule == "" {
			t.Errorf("%q metadata incomplete: %+v", key, m)
		}
	}
	for key := range metas {
		if _, ok := approvalchain.ForWorkflowKey(key); !ok {
			t.Errorf("metadata key %q is not in the approvalchain registry", key)
		}
	}
}

func TestMetaIdentifiersAreSafe(t *testing.T) {
	for _, key := range Keys() {
		cfg, _ := approvalchain.ForWorkflowKey(key)
		if msg, ok := identsOK(key, cfg, metas[key]); !ok {
			t.Errorf("unsafe identifier: %s", msg)
		}
	}
}

func TestIdentsOKRejectsInjection(t *testing.T) {
	cfg, _ := approvalchain.ForWorkflowKey("invoice")
	m := metas["invoice"]
	m.AmountColumn = "x; DROP TABLE invoice"
	if _, ok := identsOK("invoice", cfg, m); ok {
		t.Fatal("expected injected amount column to be rejected")
	}
}

func TestKeysAreSortedAndComplete(t *testing.T) {
	keys := Keys()
	if len(keys) != len(approvalchain.Keys()) {
		t.Fatalf("Keys() = %d entries, registry has %d", len(keys), len(approvalchain.Keys()))
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] >= keys[i] {
			t.Fatalf("keys not sorted: %v", keys)
		}
	}
}
