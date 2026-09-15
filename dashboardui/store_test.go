package dashboardui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// fakeRows is a pgx.Rows stand-in backed by pre-scanned column values --
// only implements what GetForIdentity's Query call paths (the
// role_permissions join and the user_roles lookup) actually exercise.
// Extends the fakeQuerier/fakeRow single-row pattern in
// salesorder/duedate_test.go to the multi-row Query case.
type fakeRows struct {
	data [][]any
	idx  int
}

func (r *fakeRows) Close()                                       {}
func (r *fakeRows) Err() error                                   { return nil }
func (r *fakeRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *fakeRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *fakeRows) Conn() *pgx.Conn                              { return nil }
func (r *fakeRows) RawValues() [][]byte                          { return nil }

func (r *fakeRows) Next() bool {
	if r.idx >= len(r.data) {
		return false
	}
	r.idx++
	return true
}

func (r *fakeRows) Values() ([]any, error) {
	return r.data[r.idx-1], nil
}

func (r *fakeRows) Scan(dest ...any) error {
	row := r.data[r.idx-1]
	for i, d := range dest {
		switch p := d.(type) {
		case *authz.Resource:
			*p = row[i].(authz.Resource)
		case *authz.Action:
			*p = row[i].(authz.Action)
		case *authz.Scope:
			*p = row[i].(authz.Scope)
		case *string:
			*p = row[i].(string)
		default:
			return fmt.Errorf("fakeRows.Scan: unsupported dest type %T", d)
		}
	}
	return nil
}

// fakeScanRow is a pgx.Row stand-in that scans a single preset []byte (the
// role_dashboard_widgets.widget_ids column) or returns a preset error --
// same shape as fakeRow in salesorder/duedate_test.go.
type fakeScanRow struct {
	raw []byte
	err error
}

func (r fakeScanRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	p, ok := dest[0].(*[]byte)
	if !ok {
		return fmt.Errorf("fakeScanRow.Scan: unsupported dest type %T", dest[0])
	}
	*p = r.raw
	return nil
}

// fakeIdentityQuerier is a minimal Querier stand-in for GetForIdentity's
// three read paths (authz.EffectiveGrants' role_permissions join,
// authz.RolesForUser's user_roles lookup, and getWidgetIDs' per-role
// role_dashboard_widgets lookup), dispatched by matching a table name in the
// SQL text.
type fakeIdentityQuerier struct {
	grants    []authz.Grant
	roleIDs   []string
	widgetIDs map[string][]string // roleID -> allocated widget ids
}

func (f *fakeIdentityQuerier) Query(_ context.Context, sql string, _ ...any) (pgx.Rows, error) {
	switch {
	case strings.Contains(sql, "role_permissions"):
		rows := make([][]any, len(f.grants))
		for i, g := range f.grants {
			rows[i] = []any{g.Resource, g.Action, g.Scope}
		}
		return &fakeRows{data: rows}, nil
	case strings.Contains(sql, "user_roles"):
		rows := make([][]any, len(f.roleIDs))
		for i, id := range f.roleIDs {
			rows[i] = []any{id}
		}
		return &fakeRows{data: rows}, nil
	default:
		return nil, fmt.Errorf("fakeIdentityQuerier.Query: unexpected sql: %s", sql)
	}
}

func (f *fakeIdentityQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if !strings.Contains(sql, "role_dashboard_widgets") {
		return fakeScanRow{err: fmt.Errorf("fakeIdentityQuerier.QueryRow: unexpected sql: %s", sql)}
	}
	roleID, _ := args[0].(string)
	ids, ok := f.widgetIDs[roleID]
	if !ok {
		return fakeScanRow{err: pgx.ErrNoRows}
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return fakeScanRow{err: err}
	}
	return fakeScanRow{raw: raw}
}

func (f *fakeIdentityQuerier) Exec(_ context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

// equalStringSets reports whether a and b contain the same elements,
// ignoring order -- GetForIdentity's result is built from a map union, so
// its iteration order is not guaranteed.
func equalStringSets(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]string{}, a...)
	sb := append([]string{}, b...)
	sort.Strings(sa)
	sort.Strings(sb)
	for i := range sa {
		if sa[i] != sb[i] {
			return false
		}
	}
	return true
}

// TestGetForIdentity_FiltersSingleGatedWidgetsByGrant covers the grant-filter
// added to GetForIdentity: a single-gated widget id (widgetRequiredResource)
// is dropped from the caller's visible set when their effective grants don't
// include a read on its required resource, and kept when they do -- the
// caller's own dashboard should never show a widget guaranteed to 403.
func TestGetForIdentity_FiltersSingleGatedWidgetsByGrant(t *testing.T) {
	ctx := context.Background()

	t.Run("single-gated widget dropped when required grant is missing", func(t *testing.T) {
		q := &fakeIdentityQuerier{
			roleIDs:   []string{"role-1"},
			widgetIDs: map[string][]string{"role-1": {"sales-orders-snapshot", "kpi-strip"}},
		}
		got, err := GetForIdentity(ctx, q, "identity-1", "user-1", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStringSets(got, []string{"kpi-strip"}) {
			t.Errorf("got %v, want [kpi-strip]", got)
		}
	})

	t.Run("single-gated widget kept when required read grant is present (scope=own is enough)", func(t *testing.T) {
		q := &fakeIdentityQuerier{
			roleIDs:   []string{"role-1"},
			widgetIDs: map[string][]string{"role-1": {"sales-orders-snapshot", "kpi-strip"}},
			grants:    []authz.Grant{{Resource: authz.ResourceSalesOrder, Action: authz.ActionRead, Scope: authz.ScopeOwn}},
		}
		got, err := GetForIdentity(ctx, q, "identity-1", "user-1", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStringSets(got, []string{"sales-orders-snapshot", "kpi-strip"}) {
			t.Errorf("got %v, want [sales-orders-snapshot kpi-strip]", got)
		}
	})

	t.Run("composable widgets are never filtered even with zero grants", func(t *testing.T) {
		q := &fakeIdentityQuerier{
			roleIDs:   []string{"role-1"},
			widgetIDs: map[string][]string{"role-1": {"kpi-strip", "pipeline-donut", "recent-records"}},
		}
		got, err := GetForIdentity(ctx, q, "identity-1", "user-1", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStringSets(got, []string{"kpi-strip", "pipeline-donut", "recent-records"}) {
			t.Errorf("got %v, want [kpi-strip pipeline-donut recent-records]", got)
		}
	})

	t.Run("wildcard grant still returns the full catalog unfiltered", func(t *testing.T) {
		q := &fakeIdentityQuerier{
			roleIDs:   []string{"role-1"},
			widgetIDs: map[string][]string{"role-1": {"sales-orders-snapshot"}},
			grants:    []authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionAny, Scope: authz.ScopeAll}},
		}
		got, err := GetForIdentity(ctx, q, "identity-1", "user-1", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !equalStringSets(got, WidgetIDs()) {
			t.Errorf("got %v, want full catalog %v", got, WidgetIDs())
		}
	})
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
