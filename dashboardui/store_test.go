package dashboardui

import (
	"testing"

	"stonesuite-backend/authz"
)

func TestIsWildcard(t *testing.T) {
	tests := []struct {
		name   string
		grants []authz.Grant
		want   bool
	}{
		{
			name:   "wildcard grant present",
			grants: []authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionAny, Scope: authz.ScopeAll}},
			want:   true,
		},
		{
			name:   "only concrete grants",
			grants: []authz.Grant{{Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll}},
			want:   false,
		},
		{
			name:   "wildcard resource but concrete action does not count",
			grants: []authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionRead, Scope: authz.ScopeAll}},
			want:   false,
		},
		{
			name:   "concrete resource but wildcard action does not count",
			grants: []authz.Grant{{Resource: authz.ResourceRole, Action: authz.ActionAny, Scope: authz.ScopeAll}},
			want:   false,
		},
		{
			name:   "no grants",
			grants: nil,
			want:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWildcard(tt.grants); got != tt.want {
				t.Errorf("isWildcard(%+v) = %v, want %v", tt.grants, got, tt.want)
			}
		})
	}
}

func TestFilterToRole(t *testing.T) {
	tests := []struct {
		name         string
		roleIDs      []string
		activeRoleID string
		want         []string
	}{
		{"active role is assigned", []string{"r1", "r2"}, "r2", []string{"r2"}},
		{"active role is not assigned", []string{"r1", "r2"}, "r3", nil},
		{"no assigned roles", nil, "r1", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterToRole(tt.roleIDs, tt.activeRoleID)
			if !equalStringSlices(got, tt.want) {
				t.Errorf("filterToRole(%v, %q) = %v, want %v", tt.roleIDs, tt.activeRoleID, got, tt.want)
			}
		})
	}
}

func TestErrInvalidWidgetIDError(t *testing.T) {
	err := &ErrInvalidWidgetID{WidgetID: "bogus-widget"}
	want := `unknown widget id "bogus-widget"`
	if got := err.Error(); got != want {
		t.Errorf("ErrInvalidWidgetID.Error() = %q, want %q", got, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
