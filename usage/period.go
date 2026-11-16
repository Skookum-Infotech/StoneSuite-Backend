package usage

import "time"

// Period is one billing period: [Start, End), both midnight UTC.
type Period struct {
	Start time.Time
	End   time.Time
}

// CurrentPeriod returns the monthly billing period containing now, with cycles
// starting on anchor's day of month. A day the month lacks (29–31) clamps to
// that month's last day, so a 31st anchor starts February's cycle on the 28th
// (or 29th). Before the anchor itself, the first period is returned.
func CurrentPeriod(anchor, now time.Time) Period {
	a := midnightUTC(anchor)
	day := a.Day()
	n := now.UTC()
	if n.Before(a) {
		return Period{Start: a, End: cycleStart(a.Year(), a.Month()+1, day)}
	}
	start := cycleStart(n.Year(), n.Month(), day)
	if start.After(n) {
		start = cycleStart(n.Year(), n.Month()-1, day)
	}
	return Period{Start: start, End: cycleStart(start.Year(), start.Month()+1, day)}
}

// cycleStart is midnight UTC on day of the given month (normalized, so month
// 13 is next January), clamped to the month's last day.
func cycleStart(year int, month time.Month, day int) time.Time {
	first := time.Date(year, month, 1, 0, 0, 0, 0, time.UTC)
	last := first.AddDate(0, 1, -1).Day()
	if day > last {
		day = last
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

// midnightUTC truncates t to the start of its UTC day.
func midnightUTC(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
