//go:build dbtest

// accountingperiod/store_dbtest_status_test.go — the close/reopen lifecycle and
// the read surface. Split from store_dbtest_test.go (which covers setup and
// generation) for the 300-line file cap, mirroring how chartofaccounts splits
// its own DB-backed tests.
package accountingperiod

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClose_EnforcesChronologicalOrder(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	fy := setupCalendarYear(t, pool)

	mar := periodByName(t, fy.Periods, "Mar 2026")
	jan := periodByName(t, fy.Periods, "Jan 2026")
	feb := periodByName(t, fy.Periods, "Feb 2026")

	// Out of order: March cannot close while January is open.
	_, err := Close(ctx, pool, []string{mar.ID}, "", 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPriorPeriodOpen), "got %v", err)

	// In order, as a batch, and books_closed_through follows.
	res, err := Close(ctx, pool, []string{jan.ID, feb.ID, mar.ID}, "Q1 close", 0)
	require.NoError(t, err)
	require.Len(t, res.Periods, 3)
	for _, p := range res.Periods {
		assert.Equal(t, StatusClosed, p.Status)
	}
	require.NotNil(t, res.BooksClosedThrough)
	assert.True(t, res.BooksClosedThrough.Equal(day(2026, 3, 31)),
		"books_closed_through = %v", res.BooksClosedThrough)
}

// TestClose_IsAllOrNothing is the guarantee that makes bulk close safe: a batch
// containing one illegal move must leave every period in the batch untouched,
// not close the legal prefix and stop.
func TestClose_IsAllOrNothing(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	fy := setupCalendarYear(t, pool)

	jan := periodByName(t, fy.Periods, "Jan 2026")
	mar := periodByName(t, fy.Periods, "Mar 2026")

	// Jan is legal, Mar is not (Feb would still be open).
	_, err := Close(ctx, pool, []string{jan.ID, mar.ID}, "", 0)
	require.Error(t, err)

	after, err := Get(ctx, pool, jan.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusOpen, after.Status, "January must not have been closed")

	cal, err := GetCalendar(ctx, pool)
	require.NoError(t, err)
	assert.Nil(t, cal.BooksClosedThrough, "the legacy column must not have moved")
}

func TestReopen_EnforcesReverseOrderAndBaseBoundary(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()

	// Go live in July, so Jan-Jun are pre-go-live and permanently closed.
	base := day(2026, 7, 1)
	fy, err := Setup(ctx, pool, SetupInput{FiscalYearStartMonth: 1, BasePeriodStart: &base}, 0)
	require.NoError(t, err)

	jul := periodByName(t, fy.Periods, "Jul 2026")
	aug := periodByName(t, fy.Periods, "Aug 2026")
	jun := periodByName(t, fy.Periods, "Jun 2026")

	_, err = Close(ctx, pool, []string{jul.ID, aug.ID}, "", 0)
	require.NoError(t, err)

	// July cannot reopen while August is closed.
	_, err = Reopen(ctx, pool, []string{jul.ID}, "", 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrLaterPeriodClosed), "got %v", err)

	// Reverse order works, and the legacy column retreats with it.
	res, err := Reopen(ctx, pool, []string{aug.ID, jul.ID}, "audit adjustment", 0)
	require.NoError(t, err)
	require.Len(t, res.Periods, 2)
	require.NotNil(t, res.BooksClosedThrough)
	assert.True(t, res.BooksClosedThrough.Equal(day(2026, 6, 30)),
		"books_closed_through = %v", res.BooksClosedThrough)

	// A pre-go-live period is never reopenable.
	_, err = Reopen(ctx, pool, []string{jun.ID}, "", 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrBeforeBasePeriod), "got %v", err)
}

// TestFiscalYearStatusIsDerived proves the year flips closed only when all
// twelve periods are, and flips back on the first reopen.
func TestFiscalYearStatusIsDerived(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	fy := setupCalendarYear(t, pool)

	all := make([]string, 0, PeriodsPerYear)
	for _, p := range fy.Periods {
		all = append(all, p.ID)
	}

	// Close everything but December: the year is still open.
	_, err := Close(ctx, pool, all[:11], "", 0)
	require.NoError(t, err)
	years, err := ListFiscalYears(ctx, pool)
	require.NoError(t, err)
	require.Len(t, years, 1)
	assert.Equal(t, StatusOpen, years[0].Status)

	_, err = Close(ctx, pool, all[11:], "", 0)
	require.NoError(t, err)
	years, err = ListFiscalYears(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, StatusClosed, years[0].Status)

	_, err = Reopen(ctx, pool, all[11:], "", 0)
	require.NoError(t, err)
	years, err = ListFiscalYears(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, StatusOpen, years[0].Status)
}

func TestListGetAndHistory(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	fy := setupCalendarYear(t, pool)
	jan := periodByName(t, fy.Periods, "Jan 2026")

	all, err := List(ctx, pool, Filters{})
	require.NoError(t, err)
	assert.Len(t, all, PeriodsPerYear)
	assert.Equal(t, "Jan 2026", all[0].Name, "listing is chronological")

	byYear, err := List(ctx, pool, Filters{FiscalYear: "FY2026", Status: StatusOpen})
	require.NoError(t, err)
	assert.Len(t, byYear, PeriodsPerYear)

	_, err = List(ctx, pool, Filters{Status: "bogus"})
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T", err)

	_, err = Get(ctx, pool, "00000000-0000-0000-0000-000000000000")
	assert.True(t, errors.Is(err, ErrNotFound), "got %v", err)

	// Generation wrote a base_setup row; closing adds a close row.
	_, err = Close(ctx, pool, []string{jan.ID}, "January close", 0)
	require.NoError(t, err)

	entries, err := History(ctx, pool, jan.ID, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(entries), 2)
	assert.Equal(t, actionClose, entries[0].Action, "newest first")
	assert.Equal(t, StatusOpen, entries[0].FromStatus)
	assert.Equal(t, StatusClosed, entries[0].ToStatus)
	assert.Equal(t, "January close", entries[0].Note)

	_, err = History(ctx, pool, "00000000-0000-0000-0000-000000000000", 0)
	assert.True(t, errors.Is(err, ErrNotFound), "unknown period should 404, got %v", err)
}

func TestForDateAndCurrent(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool)

	p, err := ForDate(ctx, pool, day(2026, 5, 17))
	require.NoError(t, err)
	assert.Equal(t, "May 2026", p.Name)

	// Boundary dates resolve to their own month, not the neighbouring one.
	first, err := ForDate(ctx, pool, day(2026, 5, 1))
	require.NoError(t, err)
	assert.Equal(t, "May 2026", first.Name)
	last, err := ForDate(ctx, pool, day(2026, 5, 31))
	require.NoError(t, err)
	assert.Equal(t, "May 2026", last.Name)

	_, err = ForDate(ctx, pool, day(2030, 1, 1))
	assert.True(t, errors.Is(err, ErrNotFound), "got %v", err)

	// Current only resolves if today happens to fall in the generated year.
	_, err = Current(ctx, pool)
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatalf("Current() = %v, want nil or ErrNotFound", err)
	}
}

// TestOverlapIsStructurallyImpossible proves the EXCLUDE constraint is present
// and doing its job, not merely declared. Go-side validation could be bypassed
// by any future code path that inserts a period; the constraint cannot.
func TestOverlapIsStructurallyImpossible(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool)

	var fyID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT fiscal_year_id FROM fiscal_year LIMIT 1`).Scan(&fyID))

	// A period overlapping March 2026 but starting on a different day, so the
	// UNIQUE(period_start) index cannot be what rejects it.
	_, err := pool.Exec(ctx, `
		INSERT INTO accounting_period (fiscal_year_id, accounting_period_name,
			period_number, period_start, period_end)
		VALUES ($1, 'Overlap', 1, $2, $3)`,
		fyID, day(2026, 3, 15), day(2026, 4, 15))
	require.Error(t, err, "an overlapping period must be rejected by the database")
}

func TestSyncBooksClosedThroughStopsAtTheFirstOpenPeriod(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	fy := setupCalendarYear(t, pool)

	jan := periodByName(t, fy.Periods, "Jan 2026")
	feb := periodByName(t, fy.Periods, "Feb 2026")

	_, err := Close(ctx, pool, []string{jan.ID, feb.ID}, "", 0)
	require.NoError(t, err)

	var through *time.Time
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT books_closed_through FROM accounting_settings WHERE accounting_settings_id = 1`,
	).Scan(&through))
	require.NotNil(t, through)
	assert.True(t, through.Equal(day(2026, 2, 28)), "got %v", through)
}
