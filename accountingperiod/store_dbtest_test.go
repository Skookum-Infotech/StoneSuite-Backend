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

// TestGenerateFiscalYear_IsForwardOnlyAndContiguous's call sites were
// mechanically adapted to unwrap GenerateResult.FiscalYears[0] after
// GenerateFiscalYear's return type changed from *FiscalYear to
// *GenerateResult; every assertion's VALUE expectation is unchanged.
func TestGenerateFiscalYear_IsForwardOnlyAndContiguous(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool)

	res27, err := GenerateFiscalYear(ctx, pool, GenerateInput{}, 0)
	require.NoError(t, err)
	require.Len(t, res27.FiscalYears, 1)
	fy27 := res27.FiscalYears[0]
	assert.Equal(t, "FY2027", fy27.Name)
	assert.True(t, fy27.Start.Equal(day(2027, 1, 1)), "start = %v", fy27.Start)
	assert.Len(t, fy27.Periods, PeriodsPerYear)

	// A mismatched confirmation year is refused rather than silently ignored.
	_, err = GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2031}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T: %v", err, err)

	// The matching one succeeds and stays contiguous.
	res28, err := GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2028}, 0)
	require.NoError(t, err)
	require.Len(t, res28.FiscalYears, 1)
	assert.True(t, res28.FiscalYears[0].Start.Equal(day(2028, 1, 1)))
}

func TestGenerateFiscalYear_RequiresSetup(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)

	_, err := GenerateFiscalYear(context.Background(), pool, GenerateInput{}, 0)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNotConfigured), "got %v", err)
}

// TestGenerateFiscalYear_Range proves a single call can generate several
// contiguous fiscal years, each with its four quarters and twelve periods,
// and that the calendar's configured start/end month come back as read-only
// context alongside them.
func TestGenerateFiscalYear_Range(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool) // FY2026

	res, err := GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2027, EndYear: 2029}, 0)
	require.NoError(t, err)
	require.Len(t, res.FiscalYears, 3)
	assert.Equal(t, 1, res.FiscalYearStartMonth)
	assert.Equal(t, 12, res.FiscalYearEndMonth)

	wantNames := []string{"FY2027", "FY2028", "FY2029"}
	for i, fy := range res.FiscalYears {
		assert.Equal(t, wantNames[i], fy.Name)
		assert.Len(t, fy.Periods, PeriodsPerYear)
		assert.Len(t, fy.Quarters, 4)
	}
	assert.True(t, res.FiscalYears[0].Start.Equal(day(2027, 1, 1)))
	assert.True(t, res.FiscalYears[2].End.Equal(day(2029, 12, 31)))
}

// TestGenerateFiscalYear_SingleYearViaEqualStartEnd proves StartYear==EndYear
// behaves identically to StartYear alone -- EndYear defaulting to StartYear
// is documented as "the same row" as the StartYear-only case (design spec
// §4), so both must produce exactly one fiscal year.
func TestGenerateFiscalYear_SingleYearViaEqualStartEnd(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool) // FY2026

	res, err := GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2027, EndYear: 2027}, 0)
	require.NoError(t, err)
	require.Len(t, res.FiscalYears, 1)
	assert.Equal(t, "FY2027", res.FiscalYears[0].Name)
}

// TestGenerateFiscalYear_RangeValidation covers the EndYear resolution table
// from the design spec: EndYear without StartYear, EndYear before StartYear,
// and a range whose StartYear does not confirm the next contiguous year are
// all client errors.
func TestGenerateFiscalYear_RangeValidation(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool) // FY2026

	_, err := GenerateFiscalYear(ctx, pool, GenerateInput{EndYear: 2028}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "endYear without startYear should be a client error, got %T: %v", err, err)

	_, err = GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2029, EndYear: 2028}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "endYear before startYear should be a client error, got %T: %v", err, err)

	// The next contiguous year is 2027, not 2028: a range starting on the
	// wrong year is refused the same way a single mismatched StartYear is.
	_, err = GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2028, EndYear: 2030}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T: %v", err, err)
}

// TestGenerateFiscalYear_RangeExceedingCapIsRefused proves maxGenerateYears
// is enforced -- a range one year over the cap is refused as a client error,
// and crucially with no year in the range generated: the check runs before
// generateYear's insert loop starts, so a caller cannot get a partial batch
// out of an over-sized request the way they could out of a mid-range
// collision (see TestGenerateFiscalYear_MidRangeCollisionRollsBackWholeBatch,
// which is the "some rows already inserted" case; this is the "no rows ever
// attempted" case).
func TestGenerateFiscalYear_RangeExceedingCapIsRefused(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool) // FY2026; next contiguous year is 2027

	endYear := 2027 + maxGenerateYears // one year past the cap
	_, err := GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2027, EndYear: endYear}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "a range over maxGenerateYears should be a client error, got %T: %v", err, err)

	years, err := ListFiscalYears(ctx, pool)
	require.NoError(t, err)
	assert.Len(t, years, 1, "only the FY2026 from setup should exist; nothing from the refused range")

	// A range exactly at the cap is unaffected by this check (it may still
	// fail later for unrelated reasons, but not on the cap itself).
	_, err = GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2027, EndYear: 2027 + maxGenerateYears - 1}, 0)
	require.NoError(t, err, "a range exactly at maxGenerateYears must be accepted")
}

// TestGenerateFiscalYear_MidRangeCollisionRollsBackWholeBatch proves the
// whole-range-is-one-transaction guarantee: a failure partway through a
// range must leave NONE of the range's years behind, not just the one that
// collided.
//
// nextFiscalYearStart always resumes from MAX(fiscal_year_end), so any
// pre-existing row positioned at (or after) the range's own years would
// itself become the new "next year" and be silently skipped past, rather
// than collided with -- the confirmation check would simply reject the
// StartYear before any insert runs. To force a genuine MID-batch failure
// (some rows already inserted by THIS call, in THIS transaction, before the
// error), the collision is planted one level deeper: a decoy fiscal_quarter
// row occupying exactly where FY2028's own Q1 would land. quarter_start is
// globally unique (uq_fq_start) regardless of which fiscal_year owns the
// row, so it collides with FY2028's real Q1 insert without disturbing
// fiscal_year's own MAX -- the decoy is anchored to a throwaway fiscal_year
// row dated safely in the past (1900) so it can never become the computed
// "next year" itself.
func TestGenerateFiscalYear_MidRangeCollisionRollsBackWholeBatch(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool) // FY2026; MAX(fiscal_year_end) = 2026-12-31

	var anchorID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO fiscal_year (fiscal_year_name, fiscal_year_start, fiscal_year_end, fiscal_year_status)
		VALUES ('FY1900-decoy-anchor', $1, $2, 'open') RETURNING fiscal_year_id`,
		day(1900, 1, 1), day(1900, 12, 31)).Scan(&anchorID))

	_, err := pool.Exec(ctx, `
		INSERT INTO fiscal_quarter (fiscal_year_id, quarter_number, quarter_name, quarter_start, quarter_end, fiscal_quarter_status)
		VALUES ($1, 1, 'decoy', $2, $3, 'open')`,
		anchorID, day(2028, 1, 1), day(2028, 3, 31))
	require.NoError(t, err)

	// StartYear:2027 still confirms correctly (MAX is untouched by the
	// 1900-dated anchor), so FY2027 -- and its quarters and periods -- are
	// inserted successfully inside this transaction before the loop reaches
	// FY2028 and collides.
	_, err = GenerateFiscalYear(ctx, pool, GenerateInput{StartYear: 2027, EndYear: 2029}, 0)
	require.Error(t, err, "FY2028's own Q1 insert must collide with the decoy's quarter_start")
	assert.True(t, IsConflict(err), "got %T: %v", err, err)

	years, err := ListFiscalYears(ctx, pool)
	require.NoError(t, err)
	names := make(map[string]bool, len(years))
	for _, fy := range years {
		names[fy.Name] = true
	}
	assert.False(t, names["FY2027"], "FY2027 must have been rolled back even though its own insert succeeded")
	assert.False(t, names["FY2028"], "FY2028 must not exist")
	assert.False(t, names["FY2029"], "FY2029 must never have been reached")
}
