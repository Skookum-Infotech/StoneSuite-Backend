package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

func testCatalog() []Widget {
	return []Widget{
		{Key: "a", Title: "A", Category: CategoryCRM, Type: TypeList,
			Resource: authz.ResourceLead, Action: authz.ActionRead, DataEndpoint: "/a",
			DefaultVisible: true, DefaultPosition: 1, DefaultWidth: 6, DefaultHeight: 4},
		{Key: "b", Title: "B", Category: CategorySales, Type: TypeList,
			Resource: authz.ResourceQuote, Action: authz.ActionRead, DataEndpoint: "/b",
			DefaultVisible: false, DefaultPosition: 0, DefaultWidth: 4, DefaultHeight: 2},
	}
}

func TestResolve(t *testing.T) {
	wildcardGrant := []authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionAny, Scope: authz.ScopeAll}}
	leadOnlyGrant := []authz.Grant{{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeOwn}}

	tests := []struct {
		name      string
		grants    []authz.Grant
		overrides map[string]bool
		prefs     map[string]UserPref
		wantKeys  []string
	}{
		{
			name:     "wildcard grant sees every enabled widget, sorted by position",
			grants:   wildcardGrant,
			wantKeys: []string{"b", "a"}, // b's DefaultPosition=0, a's=1
		},
		{
			name:     "zero grants sees nothing",
			grants:   nil,
			wantKeys: []string{},
		},
		{
			name:      "tenant-disabled hides a widget even from a wildcard grant",
			grants:    wildcardGrant,
			overrides: map[string]bool{"a": false},
			wantKeys:  []string{"b"},
		},
		{
			name:     "grant narrows to the one authorized widget",
			grants:   leadOnlyGrant,
			wantKeys: []string{"a"},
		},
		{
			name:     "pref for a retired key not in the catalog is ignored",
			grants:   wildcardGrant,
			prefs:    map[string]UserPref{"retired.widget": {WidgetKey: "retired.widget", Visible: true}},
			wantKeys: []string{"b", "a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := Resolve(testCatalog(), tt.grants, tt.overrides, tt.prefs)
			gotKeys := make([]string, len(out))
			for i, rw := range out {
				gotKeys[i] = rw.Key
			}
			assert.Equal(t, tt.wantKeys, gotKeys)
		})
	}
}

// TestResolve_SavedPrefOverridesDefault proves a saved preference replaces
// the catalog default for visible/position/width/height.
func TestResolve_SavedPrefOverridesDefault(t *testing.T) {
	wildcardGrant := []authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionAny, Scope: authz.ScopeAll}}
	prefs := map[string]UserPref{"a": {WidgetKey: "a", Visible: false, Position: 9, Width: 3, Height: 1}}

	out := Resolve(testCatalog(), wildcardGrant, nil, prefs)
	require.Len(t, out, 2)
	for _, rw := range out {
		if rw.Key != "a" {
			continue
		}
		assert.False(t, rw.Visible)
		assert.Equal(t, 9, rw.Position)
		assert.Equal(t, 3, rw.Width)
		assert.Equal(t, 1, rw.Height)
		return
	}
	t.Fatal("widget a missing from output")
}

// TestResolve_IncludesScope proves the caller's granted scope for a widget's
// resource rides through into the response, so the frontend can render
// "My Quotes" vs "All Quotes" and filter the dataEndpoint call accordingly.
func TestResolve_IncludesScope(t *testing.T) {
	ownGrant := []authz.Grant{{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeOwn}}
	out := Resolve(testCatalog(), ownGrant, nil, nil)
	require.Len(t, out, 1)
	assert.Equal(t, "own", out[0].Scope)
}
