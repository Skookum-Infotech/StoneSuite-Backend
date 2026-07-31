package accountingperiod

import (
	"fmt"
	"time"
)

// MonthSpan is one generated calendar month within a fiscal year.
type MonthSpan struct {
	Number int       // 1..12, position within the fiscal year (not the calendar year)
	Name   string    // "Jan 2026"
	Start  time.Time // first day, midnight UTC
	End    time.Time // last day, midnight UTC
}

// FirstOfMonth normalizes t to midnight UTC on the first day of its month.
// Every date this package stores is normalized this way, so a caller may pass
// any instant within the intended month.
func FirstOfMonth(t time.Time) time.Time {
	y, m, _ := t.Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}

// LastOfMonth normalizes t to midnight UTC on the last day of its month.
// Derived by stepping to the first of the next month and back one day, which
// is correct for February in leap years without a table of month lengths.
func LastOfMonth(t time.Time) time.Time {
	return FirstOfMonth(t).AddDate(0, 1, -1)
}

// ValidateStartMonth checks a fiscal-year start month is in 1..12.
func ValidateStartMonth(startMonth int) error {
	if startMonth < 1 || startMonth > int(time.December) {
		return ClientError{Msg: fmt.Sprintf(
			"fiscalYearStartMonth must be between 1 and 12, got %d.", startMonth)}
	}
	return nil
}

// FiscalYearStart returns the first day of the fiscal year that contains d.
// A date earlier in the calendar year than startMonth belongs to the fiscal
// year that began the previous calendar year.
func FiscalYearStart(d time.Time, startMonth int) time.Time {
	y, m, _ := d.Date()
	if int(m) < startMonth {
		y--
	}
	return time.Date(y, time.Month(startMonth), 1, 0, 0, 0, 0, time.UTC)
}

// FiscalYearLabel names the fiscal year beginning at start.
//
// A fiscal year that straddles a calendar-year boundary is labelled by the year
// it ENDS in — 2026-07-01..2027-06-30 is "FY2027" — which is the convention
// every major accounting package uses. A calendar fiscal year (January start)
// is labelled by its own year.
func FiscalYearLabel(start time.Time) string {
	if start.Month() == time.January {
		return fmt.Sprintf("FY%d", start.Year())
	}
	return fmt.Sprintf("FY%d", start.Year()+1)
}

// MonthsFor generates the twelve MonthSpans of the fiscal year beginning at
// start. start is normalized to the first of its month first, so AddDate never
// has to clamp a day-of-month that the target month lacks.
func MonthsFor(start time.Time) []MonthSpan {
	first := FirstOfMonth(start)
	spans := make([]MonthSpan, 0, PeriodsPerYear)
	for i := 0; i < PeriodsPerYear; i++ {
		s := first.AddDate(0, i, 0)
		spans = append(spans, MonthSpan{
			Number: i + 1,
			Name:   s.Format("Jan 2006"),
			Start:  s,
			End:    LastOfMonth(s),
		})
	}
	return spans
}

// FiscalYearEnd returns the last day of the fiscal year beginning at start.
func FiscalYearEnd(start time.Time) time.Time {
	return LastOfMonth(FirstOfMonth(start).AddDate(0, PeriodsPerYear-1, 0))
}
