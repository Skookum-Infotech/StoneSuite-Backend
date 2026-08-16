package authz

import "testing"

// TestDecide_VendorCreditApprovalSplit is the Step 11 scenario 12 test from
// docs/superpowers/plans/2026-08-13-vendor-credit-module.md, written at the
// authz-package level per that step's explicitly-permitted fallback.
//
// Why authz-level, not controller-level: the only existing controller dbtest
// harness (controllers/saml_dbtest_test.go) connects to the control-plane
// database (TEST_CP_DATABASE_URL) and calls handlers directly with a bare
// middleware.UserContextPayload in context -- it never resolves a tenant
// pool into context (tenancy.PoolFromContext's backing key,
// tenantPoolCtxKey, is unexported outside package tenancy, with no test
// helper exposing it) and never seeds a role/permission set in a tenant DB.
// Building that harness from scratch is exactly the "no clean existing
// pattern to mirror" case the plan anticipated. authz.Check itself is a thin
// wrapper around EffectiveGrants (a real DB round trip) + decide (pure); the
// package's own convention for testing permission resolution without a
// database is exercising decide() directly (see TestDecide in
// enforcer_test.go) -- this test follows that exact convention, scoped to
// the vendor_credit resource and the ActionApprove split Step 1 added.
//
// This is the concrete proof that the split gates something real:
//   - A role holding only vendor_credit:read must not be able to create.
//   - A role holding vendor_credit:update + vendor_credit:transition, but
//     NOT vendor_credit:approve, must be denied the permission a
//     DRFT->APPV transition requires (controllers.actionForTransition maps
//     toStatusCode=="APPV" to ActionApprove, every other target to
//     ActionTransition) while still being allowed a plain VOID transition
//     under the same grant set.
func TestDecide_VendorCreditApprovalSplit(t *testing.T) {
	// This is the piece that actually pins Step 1's catalog change: before it,
	// {ResourceVendorCredit, ActionApprove} was absent from catalog, so
	// IsValidPermission returned false and CreateRole/UpdateRole would have
	// rejected any attempt to grant vendor_credit:approve at all ("unknown
	// permission"). The decide()-based sub-tests below prove the split is
	// enforced correctly once a grant exists; this line proves the grant is
	// now grantable in the first place.
	if !IsValidPermission(ResourceVendorCredit, ActionApprove) {
		t.Fatal("IsValidPermission(ResourceVendorCredit, ActionApprove) = false, want true -- catalog is missing the Step 1 entry")
	}

	t.Run("read-only role cannot create", func(t *testing.T) {
		grants := []Grant{{ResourceVendorCredit, ActionRead, ScopeOwn}}
		if got := decide(grants, ResourceVendorCredit, ActionCreate); got.Allowed {
			t.Fatalf("decide(read-only, Create) = Allowed, want denied")
		}
	})

	t.Run("update+transition role is denied approve", func(t *testing.T) {
		grants := []Grant{
			{ResourceVendorCredit, ActionUpdate, ScopeOwn},
			{ResourceVendorCredit, ActionTransition, ScopeOwn},
		}
		// DRFT->APPV: controllers.actionForTransition("APPV") == ActionApprove.
		if got := decide(grants, ResourceVendorCredit, ActionApprove); got.Allowed {
			t.Fatalf("decide(update+transition only, Approve) = Allowed, want denied -- DRFT->APPV must require vendor_credit:approve")
		}
		// Plain VOID: controllers.actionForTransition("VOID") == ActionTransition,
		// which this same grant set does hold.
		if got := decide(grants, ResourceVendorCredit, ActionTransition); !got.Allowed {
			t.Fatalf("decide(update+transition, Transition) = denied, want Allowed -- a non-approval transition (e.g. VOID) must still succeed")
		}
	})

	t.Run("approve grant allows the DRFT->APPV target action", func(t *testing.T) {
		grants := []Grant{
			{ResourceVendorCredit, ActionUpdate, ScopeOwn},
			{ResourceVendorCredit, ActionTransition, ScopeOwn},
			{ResourceVendorCredit, ActionApprove, ScopeOwn},
		}
		if got := decide(grants, ResourceVendorCredit, ActionApprove); !got.Allowed {
			t.Fatalf("decide(with approve grant, Approve) = denied, want Allowed")
		}
	})
}
