package authz

import "testing"

// TestBinTransferPermissions pins the bin_transfer grant set. Without these rows
// in the catalog a permission cannot be granted at all (CreateRole/UpdateRole
// reject an unknown permission), and a module missing them compiles, boots and
// 403s at runtime.
func TestBinTransferPermissions(t *testing.T) {
	for _, a := range []Action{
		ActionCreate, ActionRead, ActionUpdate, ActionDelete, ActionTransition, ActionApprove,
	} {
		if !IsValidPermission(ResourceBinTransfer, a) {
			t.Errorf("IsValidPermission(bin_transfer, %s) = false, want true", a)
		}
	}
}

// Raising a request, completing it and signing it off are separate authorities:
// the person who asks for a move should not be the one who approves it, and a
// yard hand who can complete an approved move need not be able to approve one.
func TestBinTransferApprovalSplit(t *testing.T) {
	requester := []Grant{
		{ResourceBinTransfer, ActionCreate, ScopeOwn},
		{ResourceBinTransfer, ActionRead, ScopeOwn},
		{ResourceBinTransfer, ActionTransition, ScopeOwn},
	}
	if got := decide(requester, ResourceBinTransfer, ActionApprove); got.Allowed {
		t.Error("a role without bin_transfer:approve was allowed to approve")
	}
	if got := decide(requester, ResourceBinTransfer, ActionTransition); !got.Allowed {
		t.Error("a role holding bin_transfer:transition was denied transition")
	}

	approver := append([]Grant{}, requester...)
	approver = append(approver, Grant{ResourceBinTransfer, ActionApprove, ScopeAll})
	if got := decide(approver, ResourceBinTransfer, ActionApprove); !got.Allowed {
		t.Error("a role holding bin_transfer:approve was denied approve")
	}
}
