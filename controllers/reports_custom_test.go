package controllers

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"stonesuite-backend/authz"
	"stonesuite-backend/reports"
)

func TestParseRunLimit(t *testing.T) {
	tests := []struct {
		name    string
		in      *int
		want    int
		wantErr bool
	}{
		{"absent defaults to the preview limit", nil, reports.PreviewRowLimit, false},
		{"a normal value is kept", intPtr(50), 50, false},
		{"over the export cap is capped, not rejected", intPtr(20000), reports.ExportRowLimit, false},
		{"zero is rejected", intPtr(0), 0, true},
		{"negative is rejected", intPtr(-5), 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseRunLimit(tc.in)
			if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
				t.Fatalf("got %d, %v; want %d, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestValidateReportName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"trimmed", "  Q3 revenue  ", "Q3 revenue", false},
		{"empty", "", "", true},
		{"blank", "   ", "", true},
		{"exactly 120 characters", strings.Repeat("a", 120), strings.Repeat("a", 120), false},
		{"121 characters", strings.Repeat("a", 121), "", true},
		{"120 multi-byte characters count as 120", strings.Repeat("é", 120), strings.Repeat("é", 120), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateReportName(tc.in)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got %q, %v; want %q, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestCopyName(t *testing.T) {
	if got := copyName("Q3 revenue"); got != "Q3 revenue (copy)" {
		t.Errorf("got %q", got)
	}
	long := copyName(strings.Repeat("a", 118))
	if n := len([]rune(long)); n > reports.MaxReportName {
		t.Errorf("copy name is %d characters, max %d", n, reports.MaxReportName)
	}
	if !strings.HasSuffix(long, " (copy)") {
		t.Errorf("truncated copy name must still end with (copy): %q", long)
	}
}

func TestCustomReportErrorStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantMsg    string
	}{
		{"validation", &reports.ValidationError{Field: "x", Message: "Add at least one total."}, http.StatusBadRequest, "Add at least one total."},
		{"wrapped validation", errors.Join(errors.New("ctx"), &reports.ValidationError{Message: "bad"}), http.StatusBadRequest, "bad"},
		{"not found", reports.ErrReportNotFound, http.StatusNotFound, "Report not found."},
		{"anything else is opaque", errors.New("pq: relation does not exist"), http.StatusInternalServerError, "Could not complete that request."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, msg := customReportErrorStatus(tc.err)
			if status != tc.wantStatus || msg != tc.wantMsg {
				t.Fatalf("got %d %q, want %d %q", status, msg, tc.wantStatus, tc.wantMsg)
			}
		})
	}
}

func TestBuildCustomAccess(t *testing.T) {
	grants := []authz.Grant{
		{Resource: authz.ResourceReportSales, Action: authz.ActionRead, Scope: authz.ScopeOwn},
		{Resource: authz.ResourceReportInventory, Action: authz.ActionRead, Scope: authz.ScopeAll},
		{Resource: authz.ResourceReportCRM, Action: authz.ActionRead, Scope: authz.ScopeAll},
	}
	v2 := buildCustomAccess(grants, true)
	if !v2.allowed(authz.ResourceReportSales) || v2.allowed(authz.ResourceReportPurchases) {
		t.Error("allowed() must follow the module grants")
	}
	if v2.scope(authz.ResourceReportSales) != authz.ScopeOwn || v2.scope(authz.ResourceReportInventory) != authz.ScopeAll {
		t.Error("scope() must return each grant's own scope")
	}
	keys := map[string]bool{}
	for _, s := range v2.sources() {
		keys[s.Key] = true
	}
	for _, want := range []string{"invoices", "items", "leads"} {
		if !keys[want] {
			t.Errorf("V2 caller should see %s", want)
		}
	}
	if keys["vendors"] {
		t.Error("no purchases grant, so no vendors")
	}

	v1 := buildCustomAccess(grants, false)
	for _, s := range v1.sources() {
		if s.RequiresV2 {
			t.Errorf("V1 tenant must not see %s", s.Key)
		}
	}
	if v1.canUse(mustCustomSource(t, "leads")) {
		t.Error("canUse must refuse a CRM source on a V1 tenant")
	}
	if !v2.canUse(mustCustomSource(t, "leads")) {
		t.Error("canUse must allow a CRM source for a V2 caller with report_crm")
	}
}

func TestBuildCustomAccessSuperAdminWildcard(t *testing.T) {
	a := buildCustomAccess([]authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionAny, Scope: authz.ScopeAll}}, true)
	if len(a.sources()) != len(reports.Sources()) {
		t.Errorf("a wildcard grant should see all %d sources, got %d", len(reports.Sources()), len(a.sources()))
	}
}

func mustCustomSource(t *testing.T, key string) reports.Source {
	t.Helper()
	s, ok := reports.SourceByKey(key)
	if !ok {
		t.Fatalf("no source %q", key)
	}
	return s
}

func TestScopeArgsFor(t *testing.T) {
	if got := scopeArgsFor(authz.ScopeAll, 5); got.Restricted {
		t.Error("all scope must not restrict")
	}
	got := scopeArgsFor(authz.ScopeOwn, 5)
	if !got.Restricted || got.OwnerEmployeeID != 5 {
		t.Errorf("own scope = %+v", got)
	}
	// An identity with no employee row sees nothing: owner id 0 matches no row.
	if got := scopeArgsFor(authz.ScopeOwn, 0); !got.Restricted || got.OwnerEmployeeID != 0 {
		t.Errorf("own scope with no employee = %+v", got)
	}
}
