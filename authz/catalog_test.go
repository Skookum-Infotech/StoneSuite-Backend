package authz

import "testing"

func TestCatalogIsReturnedAsCopy(t *testing.T) {
	a := Catalog()
	if len(a) == 0 {
		t.Fatal("catalog is empty")
	}
	a[0] = Permission{Resource: "tampered", Action: "tampered"}
	b := Catalog()
	if b[0].Resource == "tampered" {
		t.Fatal("Catalog() leaked the underlying slice; mutation persisted")
	}
}

func TestIsValidPermission(t *testing.T) {
	tests := []struct {
		name string
		r    Resource
		a    Action
		want bool
	}{
		{"valid record transition", ResourceRecord, ActionTransition, true},
		{"valid lead read", ResourceLead, ActionRead, true},
		{"valid lead transition", ResourceLead, ActionTransition, true},
		{"valid prospect read", ResourceProspect, ActionRead, true},
		{"valid prospect delete", ResourceProspect, ActionDelete, true},
		{"valid configure", ResourceSSOConfig, ActionConfigure, true},
		{"valid role create", ResourceRole, ActionCreate, true},
		{"valid role delete", ResourceRole, ActionDelete, true},
		{"role does not have configure action", ResourceRole, ActionConfigure, false},
		{"resource exists but action not paired", ResourceSSOConfig, ActionTransition, false},
		{"lead does not have configure action", ResourceLead, ActionConfigure, false},
		{"prospect does not have configure action", ResourceProspect, ActionConfigure, false},
		{"unknown resource", Resource("ghost"), ActionRead, false},
		{"unknown action", ResourceRecord, Action("haunt"), false},
		{"wildcards rejected for role editing", ResourceAny, ActionAny, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsValidPermission(tc.r, tc.a); got != tc.want {
				t.Fatalf("IsValidPermission(%s,%s) = %v, want %v", tc.r, tc.a, got, tc.want)
			}
		})
	}
}

func TestIsValidScope(t *testing.T) {
	for _, s := range []Scope{ScopeAll, ScopeTeam, ScopeOwn} {
		if !IsValidScope(s) {
			t.Fatalf("scope %q should be valid", s)
		}
	}
	if IsValidScope(Scope("galaxy")) {
		t.Fatal("unknown scope should be invalid")
	}
}

func TestCatalog_InventoryItemPermissions(t *testing.T) {
	for _, a := range []Action{ActionCreate, ActionRead, ActionUpdate, ActionDelete} {
		if !IsValidPermission(ResourceInventoryItem, a) {
			t.Fatalf("inventory_item:%s must be a valid permission", a)
		}
	}
}

func TestCatalog_InvoicePermissions(t *testing.T) {
	for _, a := range []Action{ActionCreate, ActionRead, ActionUpdate, ActionDelete, ActionTransition} {
		if !IsValidPermission(ResourceInvoice, a) {
			t.Fatalf("invoice:%s must be a valid permission", a)
		}
	}
}
