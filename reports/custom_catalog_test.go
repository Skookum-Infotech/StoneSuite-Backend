package reports

import (
	"regexp"
	"testing"

	"stonesuite-backend/authz"
)

var wantSourceKeys = []string{
	"quotes", "estimates", "sales_orders", "sales_order_lines", "invoices", "invoice_lines", "payments",
	"leads", "prospects", "customers",
	"vendors", "purchase_orders", "purchase_order_lines", "vendor_bills",
	"items", "stock", "slabs",
}

var fieldKeyPattern = regexp.MustCompile(`^[a-z_]+(\.[a-z_]+)?$`)

func TestCatalogHas17SourcesWithFixedKeys(t *testing.T) {
	got := Sources()
	if len(got) != len(wantSourceKeys) {
		t.Fatalf("got %d sources, want %d", len(got), len(wantSourceKeys))
	}
	for i, s := range got {
		if s.Key != wantSourceKeys[i] {
			t.Errorf("source %d: got %q want %q", i, s.Key, wantSourceKeys[i])
		}
		if s.Label == "" || s.Module == "" || s.Resource == "" {
			t.Errorf("%s: label, module and resource must be set", s.Key)
		}
		if s.from == "" {
			t.Errorf("%s: missing FROM clause", s.Key)
		}
	}
}

func TestCatalogFieldKeysUniqueAndWellFormed(t *testing.T) {
	for _, s := range Sources() {
		seen := map[string]bool{}
		for _, f := range s.Fields {
			if !fieldKeyPattern.MatchString(f.Key) {
				t.Errorf("%s.%s: malformed key", s.Key, f.Key)
			}
			if seen[f.Key] {
				t.Errorf("%s.%s: duplicate key", s.Key, f.Key)
			}
			seen[f.Key] = true
			if f.Label == "" || f.expr == "" {
				t.Errorf("%s.%s: label and expr must be set", s.Key, f.Key)
			}
			if f.join != "" {
				if _, ok := s.joins[f.join]; !ok {
					t.Errorf("%s.%s: unknown join %q", s.Key, f.Key, f.join)
				}
			}
			if f.Type == FieldStatus && f.StatusRecordType == "" && len(f.Options) == 0 {
				t.Errorf("%s.%s: status field needs a record type or fixed options", s.Key, f.Key)
			}
			if len(OperatorsFor(f.Type)) == 0 {
				t.Errorf("%s.%s: type %q has no operators", s.Key, f.Key, f.Type)
			}
		}
		for name, j := range s.joins {
			for _, dep := range j.needs {
				if _, ok := s.joins[dep]; !ok {
					t.Errorf("%s: join %q needs unknown join %q", s.Key, name, dep)
				}
			}
		}
		if len(s.joinOrder) != len(s.joins) {
			t.Errorf("%s: joinOrder lists %d joins, source has %d", s.Key, len(s.joinOrder), len(s.joins))
		}
	}
}

var aliasPattern = regexp.MustCompile(`(?i)(?:FROM|JOIN)\s+\w+\s+(\w+)(?:\s|$)`)

func TestCatalogAliasesAreUniquePerSource(t *testing.T) {
	for _, s := range Sources() {
		seen := map[string]string{}
		for _, m := range aliasPattern.FindAllStringSubmatch(s.from, -1) {
			seen[m[1]] = "FROM"
		}
		for _, name := range s.joinOrder {
			for _, m := range aliasPattern.FindAllStringSubmatch(s.joins[name].sql, -1) {
				if prev, dup := seen[m[1]]; dup {
					t.Errorf("%s: join %q reuses alias %q already declared by %s", s.Key, name, m[1], prev)
				}
				seen[m[1]] = name
			}
		}
	}
}

func TestCatalogEverySourceHasADateField(t *testing.T) {
	for _, s := range Sources() {
		hasDate := false
		for _, f := range s.Fields {
			if f.Type == FieldDate {
				hasDate = true
			}
		}
		if !hasDate {
			t.Errorf("%s: needs at least one date field so a date range filter exists", s.Key)
		}
	}
}

func TestCatalogRealFieldsExist(t *testing.T) {
	// The fields the plan's headline examples rely on.
	inv, _ := SourceByKey("invoice_lines")
	for _, key := range []string{"net_amount", "customer.name", "customer.lead_source", "item.name", "invoice.date", "invoice.status"} {
		if _, ok := inv.Field(key); !ok {
			t.Errorf("invoice_lines is missing %q", key)
		}
	}
	if f, _ := inv.Field("net_amount"); f.Type != FieldMoney {
		t.Errorf("net_amount type = %q, want money", f.Type)
	}
}

func TestVisibleSources(t *testing.T) {
	onlySales := func(r authz.Resource) bool { return r == authz.ResourceReportSales }
	got := VisibleSources(onlySales, true)
	if len(got) != 7 {
		t.Fatalf("sales-only caller sees %d sources, want 7", len(got))
	}
	for _, s := range got {
		if s.Module != "sales" {
			t.Errorf("sales-only caller sees %s (%s)", s.Key, s.Module)
		}
	}

	all := func(authz.Resource) bool { return true }
	for _, s := range VisibleSources(all, false) {
		if s.RequiresV2 {
			t.Errorf("V1 tenant must not see %s", s.Key)
		}
	}
	if n := len(VisibleSources(all, true)); n != len(wantSourceKeys) {
		t.Errorf("V2 caller with every grant sees %d sources, want %d", n, len(wantSourceKeys))
	}
	if n := len(VisibleSources(func(authz.Resource) bool { return false }, true)); n != 0 {
		t.Errorf("caller with no grants sees %d sources, want 0", n)
	}
}

func TestOwnerScopingFollowsTheModules(t *testing.T) {
	global := map[string]bool{"items": true, "stock": true, "slabs": true}
	for _, s := range Sources() {
		if global[s.Key] && s.ownerExpr != "" {
			t.Errorf("%s is tenant-global but has an owner expression", s.Key)
		}
		if !global[s.Key] && s.ownerExpr == "" {
			t.Errorf("%s must be owner-scoped", s.Key)
		}
	}
}

func TestSourceByKey(t *testing.T) {
	if _, ok := SourceByKey("invoices"); !ok {
		t.Error("invoices should exist")
	}
	if _, ok := SourceByKey("nope"); ok {
		t.Error("unknown key should not resolve")
	}
}
