package authz

import (
	"reflect"
	"testing"
)

func TestAccessibleResourcesFromGrants(t *testing.T) {
	tests := []struct {
		name   string
		grants []Grant
		want   []string
	}{
		{
			name:   "no grants at all means no access",
			grants: nil,
			want:   []string{noAccessSentinel},
		},
		{
			name:   "a grant with no read action means no access",
			grants: []Grant{{ResourceEstimate, ActionCreate, ScopeOwn}},
			want:   []string{noAccessSentinel},
		},
		{
			name:   "single read grant",
			grants: []Grant{{ResourceEstimate, ActionRead, ScopeAll}},
			want:   []string{"estimate"},
		},
		{
			name: "read on one resource, create-only on another",
			grants: []Grant{
				{ResourceEstimate, ActionRead, ScopeAll},
				{ResourceSalesOrder, ActionCreate, ScopeOwn},
			},
			want: []string{"estimate"},
		},
		{
			name: "multiple read grants are sorted",
			grants: []Grant{
				{ResourceSalesOrder, ActionRead, ScopeOwn},
				{ResourceEstimate, ActionRead, ScopeAll},
			},
			want: []string{"estimate", "sales_order"},
		},
		{
			name:   "wildcard action counts as read",
			grants: []Grant{{ResourceInvoice, ActionAny, ScopeAll}},
			want:   []string{"invoice"},
		},
		{
			name:   "wildcard resource+action (super_admin) is unrestricted",
			grants: []Grant{{ResourceAny, ActionAny, ScopeAll}},
			want:   nil,
		},
		{
			name: "duplicate reads across roles collapse to one entry",
			grants: []Grant{
				{ResourceEstimate, ActionRead, ScopeOwn},
				{ResourceEstimate, ActionRead, ScopeAll},
			},
			want: []string{"estimate"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := accessibleResourcesFromGrants(tt.grants)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}
