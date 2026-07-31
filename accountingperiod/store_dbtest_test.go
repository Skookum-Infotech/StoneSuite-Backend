//go:build dbtest

// accountingperiod/store_dbtest_test.go — the shared harness plus the one-time
// setup and fiscal-year generation paths. The close/reopen lifecycle and the
// read surface live in store_dbtest_status_test.go.
package accountingperiod

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// freshCalendar clears every calendar row and resets the settings singleton,
// both before and after the test. Setup is single-shot by design, so each test
// that calls it needs the tenant back in its unconfigured state — the same
// state a real tenant is in before go-live, which is also the state the
// backward-compatibility fallback depends on.
func freshCalendar(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	reset := func() {
		ctx := context.Background()
		_, _ = pool.Exec(ctx, `DELETE FROM accounting_period_history`)
		_, _ = pool.Exec(ctx, `DELETE FROM accounting_period`)
		_, _ = pool.Exec(ctx, `DELETE FROM fiscal_year`)
		_, _ = pool.Exec(ctx, `
			UPDATE accounting_settings
			SET fiscal_year_start_month = NULL, base_period_start = NULL,
			    accounting_calendar_configured_at = NULL, books_closed_through = NULL
			WHERE accounting_settings_id = 1`)
	}
	reset()
	t.Cleanup(reset)
}

// setupCalendarYear configures a January-start calendar with a January base
// period, i.e. a full open FY2026.
func setupCalendarYear(t *testing.T, pool *pgxpool.Pool) *FiscalYear {
	t.Helper()
	base := day(2026, 1, 1)
	fy, err := Setup(context.Background(), pool, SetupInput{
		FiscalYearStartMonth: 1, BasePeriodStart: &base,
	}, 0)
	require.NoError(t, err)
	return fy
}

func periodByName(t *testing.T, periods []Period, name string) Period {
	t.Helper()
	for _, p := range periods {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("period %q not found", name)
	return Period{}
}

func TestSetup_GeneratesTwelveOpenPeriods(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()

	fy := setupCalendarYear(t, pool)
	assert.Equal(t, "FY2026", fy.Name)
	require.Len(t, fy.Periods, PeriodsPerYear)
	assert.True(t, fy.Start.Equal(day(2026, 1, 1)))
	assert.True(t, fy.End.Equal(day(2026, 12, 31)))

	for _, p := range fy.Periods {
		assert.Equal(t, StatusOpen, p.Status, "%s should be open", p.Name)
	}
	assert.True(t, fy.Periods[0].IsBasePeriod, "January is the base period")

	cal, err := GetCalendar(ctx, pool)
	require.NoError(t, err)
	assert.True(t, cal.Configured)
	assert.Equal(t, 1, cal.FiscalYearStartMonth)
	require.NotNil(t, cal.BasePeriodStart)
	assert.True(t, cal.BasePeriodStart.Equal(day(2026, 1, 1)))
	assert.Nil(t, cal.BooksClosedThrough, "nothing is closed yet")
}

// TestSetup_ClosesPreGoLiveMonths covers the mid-year go-live: a July base
// period in a calendar fiscal year means Jan-Jun belong to the tenant's old
// system and must arrive already closed AND already reflected in the legacy
// books_closed_through column.
func TestSetup_ClosesPreGoLiveMonths(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()

	base := day(2026, 7, 1)
	fy, err := Setup(ctx, pool, SetupInput{FiscalYearStartMonth: 1, BasePeriodStart: &base}, 0)
	require.NoError(t, err)
	require.Len(t, fy.Periods, PeriodsPerYear)

	for i, p := range fy.Periods {
		if i < 6 {
			assert.Equal(t, StatusClosed, p.Status, "%s should be closed", p.Name)
			assert.NotNil(t, p.ClosedAt, "%s should carry closed_at", p.Name)
			continue
		}
		assert.Equal(t, StatusOpen, p.Status, "%s should be open", p.Name)
	}
	assert.True(t, periodByName(t, fy.Periods, "Jul 2026").IsBasePeriod)

	cal, err := GetCalendar(ctx, pool)
	require.NoError(t, err)
	require.NotNil(t, cal.BooksClosedThrough)
	assert.True(t, cal.BooksClosedThrough.Equal(day(2026, 6, 30)),
		"books_closed_through = %v", cal.BooksClosedThrough)
}

func TestSetup_IsSingleShot(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	setupCalendarYear(t, pool)

	base := day(2026, 1, 1)
	_, err := Setup(context.Background(), pool, SetupInput{
		FiscalYearStartMonth: 7, BasePeriodStart: &base,
	}, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAlreadyConfigured), "got %v", err)
	assert.True(t, IsConflict(err), "should map to 409, got %T", err)
}

func TestSetup_RejectsBadInput(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	base := day(2026, 1, 1)

	_, err := Setup(ctx, pool, SetupInput{FiscalYearStartMonth: 13, BasePeriodStart: &base}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T", err)

	_, err = Setup(ctx, pool, SetupInput{FiscalYearStartMonth: 1}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T", err)
}

func TestGenerateFiscalYear_IsForwardOnlyAndContiguous(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool)

	fy27, err := GenerateFiscalYear(ctx, pool, GenerateInput{}, 0)
	require.NoError(t, err)
	assert.Equal(t, "FY2027", fy27.Name)
	assert.True(t, fy27.Start.Equal(day(2027, 1, 1)), "start = %v", fy27.Start)
	assert.Len(t, fy27.Periods, PeriodsPerYear)

	// A mismatched confirmation year is refused rather than silently ignored.
	_, err = GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2031}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T: %v", err, err)

	// The matching one succeeds and stays contiguous.
	fy28, err := GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2028}, 0)
	require.NoError(t, err)
	assert.True(t, fy28.Start.Equal(day(2028, 1, 1)))
}

func TestGenerateFiscalYear_RequiresSetup(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)

	_, err := GenerateFiscalYear(context.Background(), pool, GenerateInput{}, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotConfigured), "got %v", err)
}
