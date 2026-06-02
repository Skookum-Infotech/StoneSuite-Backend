package authz

import "testing"

func TestDecide(t *testing.T) {
	tests := []struct {
		name        string
		grants      []Grant
		resource    Resource
		action      Action
		wantAllowed bool
		wantScope   Scope
	}{
		{
			name:        "no grants denies",
			grants:      nil,
			resource:    ResourceRecord,
			action:      ActionRead,
			wantAllowed: false,
		},
		{
			name:        "exact match own scope",
			grants:      []Grant{{ResourceRecord, ActionRead, ScopeOwn}},
			resource:    ResourceRecord,
			action:      ActionRead,
			wantAllowed: true,
			wantScope:   ScopeOwn,
		},
		{
			name:        "wrong action denies",
			grants:      []Grant{{ResourceRecord, ActionRead, ScopeAll}},
			resource:    ResourceRecord,
			action:      ActionDelete,
			wantAllowed: false,
		},
		{
			name: "broadest scope wins across roles",
			grants: []Grant{
				{ResourceRecord, ActionRead, ScopeOwn},
				{ResourceRecord, ActionRead, ScopeAll},
				{ResourceRecord, ActionRead, ScopeTeam},
			},
			resource:    ResourceRecord,
			action:      ActionRead,
			wantAllowed: true,
			wantScope:   ScopeAll,
		},
		{
			name:        "super_admin wildcard matches anything as all",
			grants:      []Grant{{ResourceAny, ActionAny, ScopeAll}},
			resource:    ResourceSSOConfig,
			action:      ActionConfigure,
			wantAllowed: true,
			wantScope:   ScopeAll,
		},
		{
			name:        "resource wildcard with specific action",
			grants:      []Grant{{ResourceAny, ActionRead, ScopeTeam}},
			resource:    ResourceUser,
			action:      ActionRead,
			wantAllowed: true,
			wantScope:   ScopeTeam,
		},
		{
			name:        "resource wildcard does not grant other actions",
			grants:      []Grant{{ResourceAny, ActionRead, ScopeAll}},
			resource:    ResourceUser,
			action:      ActionDelete,
			wantAllowed: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := decide(tc.grants, tc.resource, tc.action)
			if got.Allowed != tc.wantAllowed {
				t.Fatalf("allowed = %v, want %v", got.Allowed, tc.wantAllowed)
			}
			if tc.wantAllowed && got.Scope != tc.wantScope {
				t.Fatalf("scope = %q, want %q", got.Scope, tc.wantScope)
			}
		})
	}
}
