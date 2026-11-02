package authz

import "testing"

// The Reorder page's permissions must be grantable from the role editor, and a
// narrower-than-all grant is never treated as wider: the controller refuses
// anything but ScopeAll, so decide() must hand back the scope the role holds.
func TestReplenishmentPermissions(t *testing.T) {
	for _, action := range []Action{ActionRead, ActionUpdate} {
		if !IsValidPermission(ResourceReplenishment, action) {
			t.Errorf("IsValidPermission(replenishment, %s) = false, want true", action)
		}
	}

	t.Run("a read grant does not allow creating drafts", func(t *testing.T) {
		grants := []Grant{{ResourceReplenishment, ActionRead, ScopeAll}}
		if got := decide(grants, ResourceReplenishment, ActionUpdate); got.Allowed {
			t.Fatal("read-only role was allowed replenishment:update")
		}
	})

	t.Run("an own-scope grant reports own, not all", func(t *testing.T) {
		grants := []Grant{{ResourceReplenishment, ActionRead, ScopeOwn}}
		got := decide(grants, ResourceReplenishment, ActionRead)
		if !got.Allowed || got.Scope != ScopeOwn {
			t.Fatalf("decide = %+v, want allowed with scope own", got)
		}
	})
}
