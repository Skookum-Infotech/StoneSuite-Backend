// Package accountingperiod owns the tenant's fiscal calendar: fiscal years,
// the monthly accounting periods under them, and the open/close lifecycle that
// gates every general-ledger write.
//
// It follows the chartofaccounts shape rather than the document-module
// clone-twin shape — tenant-global master data with no owner column, no status
// document, no lkp_record_status rows — so access control is the resource-level
// accounting_period:<action> grant with no per-record ownership scope.
//
// The read-side guard that journal.CreateEntry applies on every posting lives
// in journal/, not here: journal imports zero stonesuite-backend packages by
// design. The two packages do not import each other.
package accountingperiod

import "time"

// Status values, matching chk_ap_status and chk_fy_status in tenant/schema.sql.
const (
	StatusOpen   = "open"
	StatusClosed = "closed"
)

// PeriodsPerYear is fixed: a fiscal year is exactly twelve calendar months.
// Quarters are a reporting rollup, not a closable unit.
const PeriodsPerYear = 12

// History action verbs, matching chk_ap_history_action in tenant/schema.sql.
const (
	actionGenerate  = "generate"
	actionClose     = "close"
	actionReopen    = "reopen"
	actionBaseSetup = "base_setup"
)

// Calendar is the tenant's fiscal calendar configuration. Configured is false
// on every tenant that has never run Setup — the state in which the legacy
// books_closed_through column remains the whole period concept.
type Calendar struct {
	Configured           bool       `json:"configured"`
	FiscalYearStartMonth int        `json:"fiscalYearStartMonth,omitempty"`
	BasePeriodStart      *time.Time `json:"basePeriodStart,omitempty"`
	BooksClosedThrough   *time.Time `json:"booksClosedThrough,omitempty"`
	ConfiguredAt         *time.Time `json:"configuredAt,omitempty"`
}

// FiscalYear is one generated year. Status is derived from its periods and is
// never set directly by a caller.
type FiscalYear struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Start     time.Time `json:"start"`
	End       time.Time `json:"end"`
	Status    string    `json:"status"`
	Periods   []Period  `json:"periods,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Period is one calendar month of a fiscal year.
type Period struct {
	ID             string     `json:"id"`
	FiscalYearID   string     `json:"fiscalYearId"`
	FiscalYearName string     `json:"fiscalYearName"`
	Name           string     `json:"name"`
	Number         int        `json:"periodNumber"`
	Start          time.Time  `json:"start"`
	End            time.Time  `json:"end"`
	Status         string     `json:"status"`
	IsBasePeriod   bool       `json:"isBasePeriod"`
	ClosedAt       *time.Time `json:"closedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// SetupInput configures the fiscal calendar. It is accepted exactly once per
// tenant: reconfiguring live books would silently re-bucket every posted
// journal entry.
type SetupInput struct {
	// FiscalYearStartMonth is 1-12. 1 makes fiscal years calendar years.
	FiscalYearStartMonth int `json:"fiscalYearStartMonth"`
	// BasePeriodStart is the go-live boundary. It is normalized to the first
	// day of its month, so any date within the intended month is accepted.
	BasePeriodStart *time.Time `json:"basePeriodStart"`
}

// GenerateInput asks for one fiscal year's twelve periods to be generated.
type GenerateInput struct {
	// StartYear is the calendar year the fiscal year starts in. Zero means
	// "the year immediately following the latest one that exists".
	StartYear int `json:"startYear"`
}

// StatusChangeInput is the body of the close and reopen endpoints. A
// one-element PeriodIDs is the single-period case; there is no separate path.
type StatusChangeInput struct {
	PeriodIDs []string `json:"periodIds"`
	Note      string   `json:"note"`
}

// StatusChangeResult reports the periods that changed, plus the recomputed
// books_closed_through so a caller need not re-read the calendar.
type StatusChangeResult struct {
	Periods            []Period   `json:"periods"`
	BooksClosedThrough *time.Time `json:"booksClosedThrough"`
}

// HistoryEntry is one row of a period's audit trail.
type HistoryEntry struct {
	ID         int       `json:"id"`
	PeriodID   string    `json:"periodId"`
	Action     string    `json:"action"`
	FromStatus string    `json:"fromStatus,omitempty"`
	ToStatus   string    `json:"toStatus,omitempty"`
	Note       string    `json:"note,omitempty"`
	By         *int      `json:"by,omitempty"`
	At         time.Time `json:"at"`
}
