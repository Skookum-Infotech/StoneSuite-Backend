package reports

import (
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestWeekStartIsMonday(t *testing.T) {
	cases := []struct{ in, want time.Time }{
		{day(2026, 11, 2), day(2026, 11, 2)}, // Monday
		{day(2026, 11, 6), day(2026, 11, 2)}, // Friday
		{day(2026, 11, 8), day(2026, 11, 2)}, // Sunday belongs to the week before
		{day(2026, 11, 9), day(2026, 11, 9)}, // next Monday
		{day(2027, 1, 1), day(2026, 12, 28)}, // across a year boundary
	}
	for _, c := range cases {
		if got := weekStart(c.in); !got.Equal(c.want) {
			t.Errorf("weekStart(%v) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestWeeklyCountsFillsEmptyWeeks(t *testing.T) {
	got := weeklyCounts([]UpcomingInstall{
		{Day: day(2026, 11, 3)}, {Day: day(2026, 11, 5)}, {Day: day(2026, 11, 24)},
	})
	want := []int{2, 0, 0, 1}
	if len(got) != len(want) {
		t.Fatalf("got %d weeks, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Count != w {
			t.Errorf("week %d count = %d want %d", i, got[i].Count, w)
		}
	}
	if !got[0].WeekStart.Equal(day(2026, 11, 2)) {
		t.Errorf("first week = %v", got[0].WeekStart)
	}
	if len(weeklyCounts(nil)) != 0 {
		t.Error("no installs must give no weeks")
	}
}

func TestClampWeeks(t *testing.T) {
	cases := []struct{ in, want int }{{-3, 1}, {0, 1}, {1, 1}, {8, 8}, {26, 26}, {27, MaxUpcomingWeeks}, {1000, MaxUpcomingWeeks}}
	for _, c := range cases {
		if got := ClampWeeks(c.in); got != c.want {
			t.Errorf("ClampWeeks(%d) = %d want %d", c.in, got, c.want)
		}
	}
}

func TestSumOverdueTalliesInPlanOrder(t *testing.T) {
	got := sumOverdue([]OverdueActivity{
		{OrderID: "a", Code: "saw", DaysOverdue: 3},
		{OrderID: "a", Code: "template", DaysOverdue: 9},
		{OrderID: "b", Code: "saw", DaysOverdue: 1},
	})
	if got.Activities != 3 || got.Orders != 2 || got.Oldest != 9 {
		t.Fatalf("got %+v", got)
	}
	if len(got.ByActivity) != 2 || got.ByActivity[0].Code != "template" || got.ByActivity[1].Code != "saw" || got.ByActivity[1].Count != 2 {
		t.Errorf("by activity = %+v (template must come before saw, plan order)", got.ByActivity)
	}
	if e := sumOverdue(nil); e.Activities != 0 || e.ByActivity == nil {
		t.Errorf("empty = %+v", e)
	}
}

func TestOnTimeRowAndTotals(t *testing.T) {
	r := finishOnTimeRow(OnTimeRow{Completed: 10, OnTime: 7})
	if r.Late != 3 || !approx(r.OnTimePct, 70) {
		t.Errorf("row = %+v", r)
	}
	if finishOnTimeRow(OnTimeRow{}).OnTimePct != 0 {
		t.Error("nothing completed must not divide by zero")
	}
	tot := sumOnTime([]OnTimeRow{{Completed: 10, OnTime: 7}, {Completed: 10, OnTime: 3}})
	if tot.Completed != 20 || tot.OnTime != 10 || tot.Late != 10 || !approx(tot.OnTimePct, 50) {
		t.Errorf("totals = %+v", tot)
	}
}

func TestSumLeadTimeWeightsByOrders(t *testing.T) {
	got := sumLeadTime([]LeadTimeRow{
		{Orders: 1, AvgDay: 10, MinDay: 10, MaxDay: 10},
		{Orders: 3, AvgDay: 20, MinDay: 14, MaxDay: 30},
	})
	if got.Orders != 4 || !approx(got.AvgDay, 17.5) || got.MinDay != 10 || got.MaxDay != 30 {
		t.Errorf("got %+v", got)
	}
	if e := sumLeadTime(nil); e.Orders != 0 || e.AvgDay != 0 {
		t.Errorf("empty = %+v", e)
	}
}

func TestActivityNamesComeFromThePlanner(t *testing.T) {
	names := activityNames()
	if names["installation_ready"] != "Installation Ready" || names["saw"] != "Saw" {
		t.Errorf("names = %v", names)
	}
	if nameOrCode(names, "mystery") != "mystery" {
		t.Error("an unknown code must fall back to itself")
	}
}
