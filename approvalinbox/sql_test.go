package approvalinbox

import (
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"stonesuite-backend/authz"
)

func srcs(keys ...string) []Source {
	out := make([]Source, 0, len(keys))
	for _, k := range keys {
		out = append(out, Source{Key: k})
	}
	return out
}

var placeholderRE = regexp.MustCompile(`\$\d+`)

// Every $n used must have an argument and every argument must be used.
func assertPlaceholdersMatch(t *testing.T, sql string, args []any) {
	t.Helper()
	used := map[string]bool{}
	for _, m := range placeholderRE.FindAllString(sql, -1) {
		used[m] = true
	}
	if len(used) != len(args) {
		t.Errorf("distinct placeholders = %d, args = %d", len(used), len(args))
	}
}

func TestBuildPage(t *testing.T) {
	cur := &Cursor{EnteredAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), WorkflowKey: "invoice", UUID: goodUUID}
	tests := []struct {
		name         string
		q            pageQuery
		wantBranches int
		contains     []string
		absent       []string
	}{
		{
			name:         "mine, no cursor, one module",
			q:            pageQuery{Sources: srcs("invoice"), Scope: ScopeMine, EmployeeID: 7, Limit: 26},
			wantBranches: 1,
			contains:     []string{"invoice_approver", "NOT EXISTS (SELECT 1 FROM invoice_approval s", "ORDER BY entered_at ASC, workflow_key ASC, uuid ASC", "LIMIT $"},
			absent:       []string{"(entered_at, workflow_key, uuid) >", "d.invoice_owner_id ="},
		},
		{
			name:         "mine with cursor, two modules",
			q:            pageQuery{Sources: srcs("invoice", "refund"), Scope: ScopeMine, EmployeeID: 7, Cursor: cur, Limit: 26},
			wantBranches: 2,
			contains:     []string{"UNION ALL", "(entered_at, workflow_key, uuid) > ($"},
		},
		{
			name:         "own scope restricts by owner column",
			q:            pageQuery{Sources: []Source{{Key: "refund", OwnOnly: true}}, Scope: ScopeMine, EmployeeID: 7, Limit: 26},
			wantBranches: 1,
			contains:     []string{"d.refund_owner_id = $1"},
		},
		{
			name:         "all scope drops approver and signed predicates",
			q:            pageQuery{Sources: srcs("invoice"), Scope: ScopeAll, Limit: 26},
			wantBranches: 1,
			absent:       []string{"NOT EXISTS (SELECT 1 FROM invoice_approval s"},
		},
		{
			name:         "multi-gate module filters by gate array param",
			q:            pageQuery{Sources: srcs("installation"), Scope: ScopeMine, EmployeeID: 7, Limit: 26},
			wantBranches: 1,
			contains:     []string{"= ANY($"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args, err := buildPage(tt.q)
			if err != nil {
				t.Fatalf("buildPage: %v", err)
			}
			if got := strings.Count(sql, "UNION ALL"); got != tt.wantBranches-1 {
				t.Errorf("UNION ALL count = %d, want %d", got, tt.wantBranches-1)
			}
			for _, s := range tt.contains {
				if !strings.Contains(sql, s) {
					t.Errorf("sql missing %q", s)
				}
			}
			for _, s := range tt.absent {
				if strings.Contains(sql, s) {
					t.Errorf("sql unexpectedly contains %q", s)
				}
			}
			for _, lit := range []string{"'pending'", "'INVC'", "'PAPV'", "'RFND'"} {
				if strings.Contains(sql, lit) {
					t.Errorf("value %s inlined instead of parameterised", lit)
				}
			}
			assertPlaceholdersMatch(t, sql, args)
		})
	}
}

func TestBuildPageRejectsNonRegistryKey(t *testing.T) {
	evil := "x'; DROP TABLE invoice;--"
	if _, _, err := buildPage(pageQuery{Sources: srcs(evil), Scope: ScopeMine, Limit: 1}); err == nil {
		t.Fatal("buildPage must reject a non-registry key")
	}
}

func TestBuildPageNoSources(t *testing.T) {
	if _, _, err := buildPage(pageQuery{Scope: ScopeMine, Limit: 1}); !errors.Is(err, errNoSources) {
		t.Fatalf("err = %v, want errNoSources", err)
	}
}

func TestResolveModuleOwnScopeNeedsOwnerColumn(t *testing.T) {
	// estimate/quote/sales_order only have an owner column because of the registry back-fill.
	for _, key := range []string{"estimate", "quote", "sales_order"} {
		if _, err := resolveModule(Source{Key: key, OwnOnly: true}); err != nil {
			t.Errorf("%s must resolve under own scope: %v", key, err)
		}
	}
}

func TestBuildCountSharesPredicateWithPage(t *testing.T) {
	sources := srcs("invoice", "credit_memo")
	countSQL, countArgs, err := buildCount(sources, ScopeMine, 7)
	if err != nil {
		t.Fatal(err)
	}
	pageSQL, _, err := buildPage(pageQuery{Sources: sources, Scope: ScopeMine, EmployeeID: 7, Limit: 26})
	if err != nil {
		t.Fatal(err)
	}
	assertPlaceholdersMatch(t, countSQL, countArgs)
	for _, frag := range []string{
		"d.invoice_deleted_at IS NULL",
		"NOT EXISTS (SELECT 1 FROM invoice_approval s",
		"NOT EXISTS (SELECT 1 FROM credit_memo_approval s",
	} {
		if !strings.Contains(countSQL, frag) || !strings.Contains(pageSQL, frag) {
			t.Errorf("count and page must both contain %q", frag)
		}
	}
	if !strings.Contains(countSQL, "GROUP BY workflow_key") {
		t.Error("count must group by module")
	}
}

func TestPermitted(t *testing.T) {
	check := func(r authz.Resource, a authz.Action) authz.Decision {
		switch {
		case r == authz.ResourceInvoice:
			return authz.Decision{}
		case a == authz.ActionTransition && r == authz.ResourceQuote:
			return authz.Decision{}
		case r == authz.ResourceRefund:
			return authz.Decision{Allowed: true, Scope: authz.ScopeOwn}
		}
		return authz.Decision{Allowed: true, Scope: authz.ScopeAll}
	}
	got := Permitted(check)
	byKey := map[string]Source{}
	for _, s := range got {
		byKey[s.Key] = s
	}
	if len(got) != len(Keys())-1 {
		t.Fatalf("permitted %d modules, want %d (invoice denied)", len(got), len(Keys())-1)
	}
	if _, ok := byKey["invoice"]; ok {
		t.Error("invoice must be excluded when read is denied")
	}
	if !byKey["refund"].OwnOnly {
		t.Error("refund read scope own must set OwnOnly")
	}
	if byKey["quote"].CanTransition || !byKey["estimate"].CanTransition {
		t.Error("CanTransition must follow the transition check per module")
	}
}

func TestPermittedNothingGranted(t *testing.T) {
	if got := Permitted(func(authz.Resource, authz.Action) authz.Decision { return authz.Decision{} }); len(got) != 0 {
		t.Fatalf("permitted = %v, want none", got)
	}
}

func TestFillItemFlags(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	entered := now.Add(-73 * time.Hour)
	tests := []struct {
		name                    string
		key, status             string
		isApprover, transition  bool
		superAdmin              bool
		wantApprove, wantReject bool
	}{
		{"approver with transition on rejectable gate", "invoice", "PAPV", true, true, false, true, true},
		{"approver but no transition permission", "invoice", "PAPV", true, false, false, false, false},
		{"not an approver", "invoice", "PAPV", false, true, false, false, false},
		{"super admin override", "invoice", "PAPV", false, true, true, true, true},
		{"gate without reject support", "installation", "TMPL", true, true, false, true, false},
		{"in-place reject gate", "refund", "PEND", true, true, false, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			it := Item{WorkflowKey: tt.key, StatusCode: tt.status, EnteredAt: entered}
			fillItem(&it, tt.isApprover, tt.transition, tt.superAdmin, now)
			if it.CanApprove != tt.wantApprove || it.CanReject != tt.wantReject {
				t.Errorf("canApprove/canReject = %v/%v, want %v/%v", it.CanApprove, it.CanReject, tt.wantApprove, tt.wantReject)
			}
			if it.AgeDays != 3 || it.Label == "" || it.Domain == "" {
				t.Errorf("registry fields not filled: %+v", it)
			}
		})
	}
}

func TestCheckScope(t *testing.T) {
	if err := checkScope(Caller{}, ScopeAll); err == nil {
		t.Error("non-super-admin scope=all must be refused")
	}
	if err := checkScope(Caller{IsSuperAdmin: true}, ScopeAll); err != nil {
		t.Errorf("super admin scope=all: %v", err)
	}
	if err := checkScope(Caller{}, Scope("bogus")); err == nil {
		t.Error("unknown scope must be refused")
	}
}
