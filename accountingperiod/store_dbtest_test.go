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
		_, _ = pool.Exec(ctx, `DELETE FROM fiscal_quarter`)
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

// TestGenerateFiscalYear_IsForwardOnlyAndContiguous's call sites unwrap
// GenerateResult.FiscalYears[0]: GenerateFiscalYear's return type is
// *GenerateResult, not *FiscalYear, but every assertion's VALUE expectation
// is unchanged.
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

// TestGenerateFiscalYear_MultipleYearsAreContiguous covers the batch case:
// Years > 1 generates several years back-to-back in the one call, not just
// the same next year repeated.
func TestGenerateFiscalYear_MultipleYearsAreContiguous(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool) // FY2026 already exists

	res, err := GenerateFiscalYear(ctx, pool, GenerateInput{Years: 3}, 0)
	require.NoError(t, err)
	require.Len(t, res.FiscalYears, 3)

	wantNames := []string{"FY2027", "FY2028", "FY2029"}
	for i, fy := range res.FiscalYears {
		assert.Equal(t, wantNames[i], fy.Name)
		assert.True(t, fy.Start.Equal(day(2026+i+1, 1, 1)), "year %d start = %v", i, fy.Start)
		assert.Len(t, fy.Periods, PeriodsPerYear, "year %d", i)
	}

	// The next call continues from FY2030, proving the batch really committed
	// three contiguous years rather than repeating the same start.
	next, err := GenerateFiscalYear(ctx, pool, GenerateInput{}, 0)
	require.NoError(t, err)
	require.Len(t, next.FiscalYears, 1)
	assert.Equal(t, "FY2030", next.FiscalYears[0].Name)
}

// GenerateFiscalYear always starts its batch at MAX(fiscal_year_end)+1, so a
// synchronous pre-insert of a future year (e.g. FY2028) can never collide
// with the batch -- it just becomes the new frontier and the batch starts
// after it. A real collision only happens when a second, already-committed
// writer beats this transaction to the exact next year while it is mid-batch
// -- reproduced here by driving generateYear directly (the same call
// GenerateFiscalYear's loop makes) inside a transaction, exactly as
// GenerateFiscalYear does, then having a second connection win FY2028 first.
func TestGenerateFiscalYear_MultiYearFailureRollsBackWholeBatch(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool) // FY2026

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = generateYear(ctx, tx, day(2027, 1, 1), nil, actionGenerate, 0)
	require.NoError(t, err, "FY2027 generates cleanly, same as the batch's first year")

	// A concurrent request wins FY2028 first, over its own connection, and
	// commits before this transaction gets there.
	_, err = pool.Exec(ctx, `
		INSERT INTO fiscal_year (fiscal_year_name, fiscal_year_start, fiscal_year_end)
		VALUES ('FY2028', $1, $2)`, day(2028, 1, 1), day(2028, 12, 31))
	require.NoError(t, err)

	_, err = generateYear(ctx, tx, day(2028, 1, 1), nil, actionGenerate, 0)
	require.Error(t, err, "FY2028 must collide with the concurrently committed row")
	_ = tx.Rollback(ctx)

	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM fiscal_year WHERE fiscal_year_name = 'FY2027'`).Scan(&count))
	assert.Equal(t, 0, count, "FY2027 must roll back along with the rest of the failed batch")
}

// TestGenerateFiscalYear_RejectsOutOfBoundsYears covers Years-specific input
// validation: negative counts and counts over the shared maxGenerateYears cap.
func TestGenerateFiscalYear_RejectsOutOfBoundsYears(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool)

	_, err := GenerateFiscalYear(ctx, pool, GenerateInput{Years: -1}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T: %v", err, err)

	_, err = GenerateFiscalYear(ctx, pool, GenerateInput{Years: maxGenerateYears + 1}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T: %v", err, err)
}

// TestGenerateFiscalYear_RejectsBothEndYearAndYears covers the mutual
// exclusivity between the two range-selection inputs.
func TestGenerateFiscalYear_RejectsBothEndYearAndYears(t *testing.T) {
	pool := testPool(t)
	freshCalendar(t, pool)
	ctx := context.Background()
	setupCalendarYear(t, pool)

	_, err := GenerateFiscalYear(ctx, pool, GenerateInput{EndYear: 2030, Years: 3}, 0)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "got %T: %v", err, err)
}
