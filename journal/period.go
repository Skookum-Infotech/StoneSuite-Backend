package journal

import "time"

// isClosed reports whether effectiveDate falls on or before closedThrough,
// comparing calendar dates only (the time-of-day component is ignored, since
// closedThrough is always midnight and effectiveDate may not be — e.g. a
// reversal's effective date is time.Now()).
func isClosed(effectiveDate, closedThrough time.Time) bool {
	ey, em, ed := effectiveDate.Date()
	cy, cm, cd := closedThrough.Date()
	e := time.Date(ey, em, ed, 0, 0, 0, 0, time.UTC)
	c := time.Date(cy, cm, cd, 0, 0, 0, 0, time.UTC)
	return !e.After(c)
}

// isBefore reports whether d falls strictly before boundary, comparing
// calendar dates only — the same time-of-day-insensitive comparison isClosed
// makes, for the same reason.
func isBefore(d, boundary time.Time) bool {
	dy, dm, dd := d.Date()
	by, bm, bd := boundary.Date()
	return time.Date(dy, dm, dd, 0, 0, 0, 0, time.UTC).
		Before(time.Date(by, bm, bd, 0, 0, 0, 0, time.UTC))
}

// Period status values, matching chk_ap_status in tenant/schema.sql.
const (
	statusOpen   = "open"
	statusClosed = "closed"
)

// periodVerdict is the decision CheckPeriodOpen turns into an error (or into
// nil). Keeping it a distinct type — rather than a bare bool — is what lets
// "closed" and "no period exists at all" carry different messages, which is
// the difference between "reopen September" and "generate FY2027".
type periodVerdict int

const (
	verdictOpen periodVerdict = iota
	verdictClosed
	verdictNoPeriod
)

// periodLookup is the raw database state the verdict is derived from. It is a
// plain struct so the decision itself stays a pure, table-driven-testable
// function with no database access — the same split isClosed already follows.
type periodLookup struct {
	// Found reports whether a row in accounting_period covers the date.
	Found bool
	// Status is that row's gl_lock_status -- the GL choke point specifically,
	// not the derived overall accounting_period_status (see lookupPeriod in
	// store.go). Meaningless when !Found.
	Status string
	// CalendarExists reports whether the tenant has ANY accounting_period row,
	// i.e. whether they have ever run the base period setup.
	CalendarExists bool
	// BasePeriodStart is accounting_settings.base_period_start — the go-live
	// boundary. nil when the calendar was never configured.
	BasePeriodStart *time.Time
	// ClosedThrough is accounting_settings.books_closed_through, the legacy
	// single-date period concept. Still the answer for tenants with no
	// calendar.
	ClosedThrough *time.Time
}

// verdictFor decides whether effectiveDate may be posted to.
//
// The ordering here is the backward-compatibility contract. A tenant that has
// never configured a fiscal calendar has no accounting_period rows at all, so
// every date falls through to the books_closed_through comparison and behaves
// EXACTLY as it did before this module existed. The stricter period rules
// switch on per tenant, at the moment they run setup — never retroactively.
func verdictFor(effectiveDate time.Time, l periodLookup) periodVerdict {
	if l.Found {
		if l.Status == statusClosed {
			return verdictClosed
		}
		return verdictOpen
	}

	// No calendar has ever been configured: fall back to the legacy column.
	if !l.CalendarExists {
		if l.ClosedThrough == nil {
			return verdictOpen
		}
		if isClosed(effectiveDate, *l.ClosedThrough) {
			return verdictClosed
		}
		return verdictOpen
	}

	// A calendar exists but does not cover this date. Before the go-live
	// boundary the books are closed permanently; after the last generated
	// period they are not closed, they are simply not there yet — and those
	// need different messages to be actionable.
	if l.BasePeriodStart != nil && isBefore(effectiveDate, *l.BasePeriodStart) {
		return verdictClosed
	}
	return verdictNoPeriod
}
