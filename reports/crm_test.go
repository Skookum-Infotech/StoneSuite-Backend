package reports

import (
	"context"
	"math"
	"strings"
	"testing"

	"stonesuite-backend/authz"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBuildFunnel(t *testing.T) {
	days := 6.5
	f := buildFunnel(funnelCounts{
		leads: 20, qualified: 10, unqualified: 4, prospects: 5, customers: 2,
		daysToProspect: &days,
	})
	if f.Unqualified != 4 || len(f.Stages) != 4 {
		t.Fatalf("got %+v", f)
	}
	want := []struct {
		key        string
		count      int
		ofLeads    float64
		ofPrevious float64
	}{
		{"leads", 20, 100, 100},
		{"qualified", 10, 50, 50},
		{"prospects", 5, 25, 50},
		{"customers", 2, 10, 40},
	}
	for i, w := range want {
		s := f.Stages[i]
		if s.Key != w.key || s.Count != w.count || !approx(s.PercentOfLeads, w.ofLeads) || !approx(s.PercentOfPrev, w.ofPrevious) {
			t.Errorf("stage %d = %+v, want %+v", i, s, w)
		}
	}
	if f.Stages[2].AvgDaysFromLead == nil || *f.Stages[2].AvgDaysFromLead != days {
		t.Errorf("prospect stage avg days = %v", f.Stages[2].AvgDaysFromLead)
	}
	if f.Stages[3].AvgDaysFromLead != nil || f.Stages[0].AvgDaysFromLead != nil {
		t.Error("only reached stages with data carry an average")
	}
}

func TestBuildFunnelEmptyCohortHasNoNaN(t *testing.T) {
	f := buildFunnel(funnelCounts{})
	for _, s := range f.Stages {
		if s.Count != 0 || s.PercentOfLeads != 0 || s.PercentOfPrev != 0 {
			t.Errorf("stage %s = %+v, want all zero", s.Key, s)
		}
	}
}

func TestSourceRowsAndTotals(t *testing.T) {
	rows := []SourceRow{
		finishSourceRow(SourceRow{Source: "Referral", Leads: 4, Customers: 2, Revenue: 1000}),
		finishSourceRow(SourceRow{Source: UnspecifiedSource, Leads: 6, Customers: 0, Revenue: 0}),
	}
	if !approx(rows[0].ConversionPct, 50) || !approx(rows[0].RevenuePerLead, 250) {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[1].ConversionPct != 0 || rows[1].RevenuePerLead != 0 {
		t.Errorf("row 1 = %+v", rows[1])
	}
	tot := sumSources(rows)
	if tot.Leads != 10 || tot.Customers != 2 || !approx(tot.ConversionPct, 20) || !approx(tot.Revenue, 1000) {
		t.Errorf("totals = %+v", tot)
	}
	if got := sumSources(nil); got.ConversionPct != 0 {
		t.Errorf("empty totals = %+v", got)
	}
}

func TestClampStaleDays(t *testing.T) {
	cases := []struct{ in, want int }{
		{-5, MinStaleDays}, {0, MinStaleDays}, {1, 1}, {14, 14}, {365, 365}, {366, MaxStaleDays}, {99999, MaxStaleDays},
	}
	for _, c := range cases {
		if got := ClampStaleDays(c.in); got != c.want {
			t.Errorf("ClampStaleDays(%d) = %d want %d", c.in, got, c.want)
		}
	}
}

func TestLeadCohortSQLScopesAndWindows(t *testing.T) {
	cte, args, ok, err := leadCohort(context.Background(), nil, CRMFilter{Scope: authz.ScopeAll})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(args) != 1 {
		t.Errorf("unbounded all-scope cohort should only bind the billed codes, got %v", args)
	}
	for _, want := range []string{"lead_cohort", "descendants", "journey", "rev", "customer_parent_id", "ANY($1)"} {
		if !strings.Contains(cte, want) {
			t.Errorf("CTE missing %q", want)
		}
	}
}
