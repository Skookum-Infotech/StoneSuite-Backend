package reports

import (
	"errors"
	"strings"
	"testing"
)

func mustSource(t *testing.T, key string) Source {
	t.Helper()
	s, ok := SourceByKey(key)
	if !ok {
		t.Fatalf("no source %q", key)
	}
	return s
}

func groupedByCustomer() Definition {
	return Definition{
		Source:  "invoice_lines",
		GroupBy: &GroupBy{Field: "customer.name"},
		Totals:  []Total{{Fn: AggSum, Field: "net_amount"}},
		Chart:   ChartBar,
	}
}

func ungroupedList(filters ...FilterRule) Definition {
	return Definition{
		Source:  "invoice_lines",
		Columns: []string{"invoice.number", "customer.name", "net_amount"},
		Filters: filters,
		Chart:   ChartNone,
	}
}

func TestBuildQueryGrouped(t *testing.T) {
	c, err := BuildQuery(mustSource(t, "invoice_lines"), groupedByCustomer(), ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "GROUP BY 1") {
		t.Errorf("missing GROUP BY 1:\n%s", c.SQL)
	}
	if n := strings.Count(c.SQL, "LEFT JOIN customer c"); n != 1 {
		t.Errorf("customer join appears %d times, want 1", n)
	}
	if !strings.Contains(c.SQL, "LIMIT 501") {
		t.Errorf("want LIMIT 501 (limit+1 to detect truncation):\n%s", c.SQL)
	}
	if c.Limit != PreviewRowLimit || c.TopN {
		t.Errorf("Limit = %d TopN = %v, want %d false", c.Limit, c.TopN, PreviewRowLimit)
	}
	if len(c.Columns) != 2 || c.Columns[0].Key != "group" || c.Columns[1].Key != "total:0" {
		t.Fatalf("columns = %+v, want [group total:0]", c.Columns)
	}
	if c.Columns[1].Type != FieldMoney || c.Columns[1].Label != "Sum of Net amount" {
		t.Errorf("total column = %+v", c.Columns[1])
	}
}

func TestBuildQueryValuesNeverInSQL(t *testing.T) {
	hostile := "'; DROP TABLE invoice;--"
	c, err := BuildQuery(mustSource(t, "invoice_lines"),
		ungroupedList(FilterRule{Field: "customer.name", Op: OpEq, Values: []string{hostile}}), ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.SQL, "DROP") || strings.Contains(c.TotalsSQL, "DROP") {
		t.Fatalf("user value leaked into SQL:\n%s", c.SQL)
	}
	found := false
	for _, a := range c.Args {
		if a == hostile {
			found = true
		}
	}
	if !found {
		t.Errorf("hostile value should travel as a bind arg, args = %v", c.Args)
	}
}

func TestBuildQueryOwnerClause(t *testing.T) {
	restricted := ScopeArgs{Restricted: true, OwnerEmployeeID: 7}
	c, err := BuildQuery(mustSource(t, "invoices"), Definition{Source: "invoices", Columns: []string{"number"}, Chart: ChartNone}, restricted, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "h.invoice_owner_id = $1") || len(c.Args) != 1 || c.Args[0] != 7 {
		t.Errorf("owner clause missing; sql:\n%s\nargs: %v", c.SQL, c.Args)
	}

	// Inventory is tenant-global: a restricted caller still gets no owner clause.
	st, err := BuildQuery(mustSource(t, "stock"), Definition{Source: "stock", Columns: []string{"on_hand"}, Chart: ChartNone}, restricted, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(st.SQL, "owner") || len(st.Args) != 0 {
		t.Errorf("stock must not be owner-scoped; sql:\n%s\nargs: %v", st.SQL, st.Args)
	}

	// An unrestricted caller gets none either.
	all, _ := BuildQuery(mustSource(t, "invoices"), Definition{Source: "invoices", Columns: []string{"number"}, Chart: ChartNone}, ScopeArgs{}, PreviewRowLimit)
	if strings.Contains(all.SQL, "invoice_owner_id") {
		t.Errorf("unrestricted scope added an owner clause:\n%s", all.SQL)
	}
}

func TestBuildQueryDateRangeIsInclusive(t *testing.T) {
	c, err := BuildQuery(mustSource(t, "invoice_lines"),
		ungroupedList(FilterRule{Field: "invoice.date", Op: OpDateRange, Values: []string{"2026-01-01", "2026-01-31"}}), ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, ">= $1::date") || !strings.Contains(c.SQL, "< $2::date + 1") {
		t.Errorf("date range should be [from, to+1):\n%s", c.SQL)
	}
	if c.Args[0] != "2026-01-01" || c.Args[1] != "2026-01-31" {
		t.Errorf("args = %v", c.Args)
	}
}

func TestBuildQueryBlankGroup(t *testing.T) {
	c, err := BuildQuery(mustSource(t, "invoice_lines"), groupedByCustomer(), ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "'"+BlankGroupLabel+"'") {
		t.Errorf("NULL/empty groups should read %q:\n%s", BlankGroupLabel, c.SQL)
	}
}

func TestBuildQueryTopN(t *testing.T) {
	def := groupedByCustomer()
	def.TopN = 10
	c, err := BuildQuery(mustSource(t, "invoice_lines"), def, ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "LIMIT 11") || c.Limit != 10 || !c.TopN {
		t.Errorf("top 10 should LIMIT 11 with Limit=10 TopN=true; got Limit=%d TopN=%v\n%s", c.Limit, c.TopN, c.SQL)
	}
	// A top-N bigger than the row limit does not raise the limit.
	def.TopN = 100
	c, _ = BuildQuery(mustSource(t, "invoice_lines"), def, ScopeArgs{}, 50)
	if c.Limit != 50 || c.TopN {
		t.Errorf("limit 50 must win over top 100; got Limit=%d TopN=%v", c.Limit, c.TopN)
	}
}

func TestBuildQueryRejectsInvalid(t *testing.T) {
	def := groupedByCustomer()
	def.Totals = nil
	_, err := BuildQuery(mustSource(t, "invoice_lines"), def, ScopeArgs{}, PreviewRowLimit)
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
}

func TestBuildQueryJoinsAreOrderedAndDeduplicated(t *testing.T) {
	def := Definition{
		Source:  "invoice_lines",
		GroupBy: &GroupBy{Field: "customer.lead_source"},
		Totals:  []Total{{Fn: AggSum, Field: "net_amount"}},
		Filters: []FilterRule{{Field: "customer.name", Op: OpContains, Values: []string{"a"}}},
		Chart:   ChartBar,
	}
	c, err := BuildQuery(mustSource(t, "invoice_lines"), def, ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	cust := strings.Index(c.SQL, "LEFT JOIN customer c")
	src := strings.Index(c.SQL, "LEFT JOIN lkp_crm_lead_source cls")
	if cust < 0 || src < 0 || cust > src {
		t.Errorf("customer join must precede the join that hangs off it:\n%s", c.SQL)
	}
	if strings.Count(c.SQL, "LEFT JOIN customer c") != 1 {
		t.Errorf("customer joined more than once:\n%s", c.SQL)
	}
	if strings.Contains(c.SQL, "lkp_state") {
		t.Errorf("unused joins must not be emitted:\n%s", c.SQL)
	}
}

func TestBuildQueryContainsEscapesLikeWildcards(t *testing.T) {
	c, err := BuildQuery(mustSource(t, "invoice_lines"),
		ungroupedList(FilterRule{Field: "customer.name", Op: OpContains, Values: []string{`50%_off\`}}), ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, `ILIKE '%' || $1 || '%' ESCAPE '\'`) {
		t.Errorf("contains must be an escaped ILIKE:\n%s", c.SQL)
	}
	if c.Args[0] != `50\%\_off\\` {
		t.Errorf("arg = %q, want wildcards escaped", c.Args[0])
	}
}

func TestBuildQueryNumbersAreCastNotInterpolated(t *testing.T) {
	c, err := BuildQuery(mustSource(t, "invoice_lines"),
		ungroupedList(FilterRule{Field: "net_amount", Op: OpBetween, Values: []string{" 10 ", "1000.5"}}), ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "BETWEEN $1::numeric AND $2::numeric") {
		t.Errorf("number filters must bind numerics:\n%s", c.SQL)
	}
	if c.Args[0] != "10" || c.Args[1] != "1000.5" {
		t.Errorf("args = %v, want trimmed strings", c.Args)
	}
}

func TestBuildQueryStatusIn(t *testing.T) {
	c, err := BuildQuery(mustSource(t, "invoice_lines"),
		ungroupedList(FilterRule{Field: "invoice.status", Op: OpIn, Values: []string{"Sent", "Paid"}}), ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "= ANY($1)") {
		t.Errorf("status filter should be = ANY:\n%s", c.SQL)
	}
	got, ok := c.Args[0].([]string)
	if !ok || len(got) != 2 || got[0] != "Sent" {
		t.Errorf("arg = %#v, want []string{Sent Paid}", c.Args[0])
	}
}

func TestBuildQueryIsEmptyAndNotEqual(t *testing.T) {
	c, err := BuildQuery(mustSource(t, "invoice_lines"), ungroupedList(
		FilterRule{Field: "customer.city", Op: OpIsEmpty},
		FilterRule{Field: "customer.name", Op: OpNeq, Values: []string{"Acme"}},
	), ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "IS NULL OR") || !strings.Contains(c.SQL, "IS DISTINCT FROM $1") {
		t.Errorf("is_empty / neq SQL wrong:\n%s", c.SQL)
	}
	if len(c.Args) != 1 {
		t.Errorf("is_empty takes no arg; args = %v", c.Args)
	}
}

func TestBuildQueryDateBucket(t *testing.T) {
	def := Definition{
		Source:  "invoice_lines",
		GroupBy: &GroupBy{Field: "invoice.date", DateBucket: DateBucketMonth},
		Totals:  []Total{{Fn: AggCount}},
		Chart:   ChartBar,
	}
	c, err := BuildQuery(mustSource(t, "invoice_lines"), def, ScopeArgs{}, PreviewRowLimit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.SQL, "date_trunc('month'") {
		t.Errorf("month bucket missing:\n%s", c.SQL)
	}
	if !strings.Contains(c.SQL, "ORDER BY 1 ASC") {
		t.Errorf("a date group defaults to chronological order:\n%s", c.SQL)
	}
	if c.Columns[0].Type != FieldDate || c.Columns[1].Label != "Count" || c.Columns[1].Type != FieldNumber {
		t.Errorf("columns = %+v", c.Columns)
	}
}

func TestBuildQuerySorting(t *testing.T) {
	c, _ := BuildQuery(mustSource(t, "invoice_lines"), groupedByCustomer(), ScopeArgs{}, PreviewRowLimit)
	if !strings.Contains(c.SQL, "ORDER BY 2 DESC NULLS LAST, 1") {
		t.Errorf("grouped default is the first total, biggest first:\n%s", c.SQL)
	}

	def := groupedByCustomer()
	def.Sort = &SortSpec{Key: "group"}
	c, _ = BuildQuery(mustSource(t, "invoice_lines"), def, ScopeArgs{}, PreviewRowLimit)
	if !strings.Contains(c.SQL, "ORDER BY 1 ASC NULLS LAST") {
		t.Errorf("sort by group:\n%s", c.SQL)
	}

	list := ungroupedList()
	list.Sort = &SortSpec{Key: "net_amount", Desc: true}
	c, _ = BuildQuery(mustSource(t, "invoice_lines"), list, ScopeArgs{}, PreviewRowLimit)
	if !strings.Contains(c.SQL, "ORDER BY 3 DESC NULLS LAST") {
		t.Errorf("sort by the third column:\n%s", c.SQL)
	}
}

func TestBuildQueryTotalsSQL(t *testing.T) {
	g, _ := BuildQuery(mustSource(t, "invoice_lines"), groupedByCustomer(), ScopeArgs{}, PreviewRowLimit)
	if strings.Contains(g.TotalsSQL, "GROUP BY") || strings.Contains(g.TotalsSQL, "LIMIT") {
		t.Errorf("grouped totals cover every row, no GROUP BY/LIMIT:\n%s", g.TotalsSQL)
	}
	u, _ := BuildQuery(mustSource(t, "invoice_lines"), ungroupedList(), ScopeArgs{}, PreviewRowLimit)
	if !strings.HasPrefix(u.TotalsSQL, "SELECT COUNT(*)") {
		t.Errorf("ungrouped totals lead with the row count:\n%s", u.TotalsSQL)
	}
	if strings.Contains(u.TotalsSQL, "ORDER BY") {
		t.Errorf("totals need no ordering:\n%s", u.TotalsSQL)
	}
}

func TestBuildQueryEverySourceCompiles(t *testing.T) {
	// A list of every field and a grouped count per source: nothing in the
	// catalog may fail to compile or reference a join that does not exist.
	for _, s := range Sources() {
		cols := make([]string, 0, MaxColumns)
		for _, f := range s.Fields {
			if len(cols) < MaxColumns {
				cols = append(cols, f.Key)
			}
		}
		if _, err := BuildQuery(s, Definition{Source: s.Key, Columns: cols, Chart: ChartNone}, ScopeArgs{}, PreviewRowLimit); err != nil {
			t.Errorf("%s list: %v", s.Key, err)
		}
		for _, f := range s.Fields {
			g := &GroupBy{Field: f.Key}
			if f.Type == FieldDate {
				g.DateBucket = DateBucketMonth
			}
			def := Definition{Source: s.Key, GroupBy: g, Totals: []Total{{Fn: AggCount}}, Chart: ChartBar}
			if _, err := BuildQuery(s, def, ScopeArgs{}, PreviewRowLimit); err != nil {
				t.Errorf("%s group by %s: %v", s.Key, f.Key, err)
			}
		}
	}
}
