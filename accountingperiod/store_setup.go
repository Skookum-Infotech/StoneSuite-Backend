package accountingperiod

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// calendarColumns is the shared projection for accounting_settings.
const calendarColumns = `
	SELECT fiscal_year_start_month, base_period_start,
	       books_closed_through, accounting_calendar_configured_at
	FROM accounting_settings WHERE accounting_settings_id = 1`

// scanCalendar reads the settings row into a Calendar. Configured is derived
// from accounting_calendar_configured_at rather than from the presence of a
// start month, so a half-written row cannot read as configured.
func scanCalendar(row pgx.Row) (*Calendar, error) {
	var (
		c          Calendar
		startMonth *int
	)
	if err := row.Scan(&startMonth, &c.BasePeriodStart,
		&c.BooksClosedThrough, &c.ConfiguredAt); err != nil {
		return nil, err
	}
	if startMonth != nil {
		c.FiscalYearStartMonth = *startMonth
	}
	c.Configured = c.ConfiguredAt != nil
	return &c, nil
}

// GetCalendar returns the tenant's fiscal calendar configuration.
func GetCalendar(ctx context.Context, pool *pgxpool.Pool) (*Calendar, error) {
	c, err := scanCalendar(pool.QueryRow(ctx, calendarColumns))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoSettingsRow
	}
	if err != nil {
		return nil, fmt.Errorf("load fiscal calendar: %w", err)
	}
	return c, nil
}

// lockCalendar reads the settings row FOR UPDATE inside a transaction.
//
// The lock is what makes Setup single-shot under concurrency: two simultaneous
// setup calls serialize on this row, and the second one then observes the
// first's committed accounting_calendar_configured_at and refuses, instead of
// both generating a fiscal year and colliding on uq_fiscal_year_start with a
// 500 rather than a clean 409.
func lockCalendar(ctx context.Context, tx pgx.Tx) (*Calendar, error) {
	c, err := scanCalendar(tx.QueryRow(ctx, calendarColumns+` FOR UPDATE`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoSettingsRow
	}
	if err != nil {
		return nil, fmt.Errorf("lock fiscal calendar: %w", err)
	}
	return c, nil
}

// Setup configures the fiscal calendar once and generates the fiscal year
// containing the base period.
//
// It is deliberately not idempotent: a second call returns ErrAlreadyConfigured
// rather than reconfiguring. Changing a live tenant's fiscal-year start month
// would silently re-bucket every journal entry already posted, and there is no
// safe automatic answer to what that should mean.
func Setup(ctx context.Context, pool *pgxpool.Pool, in SetupInput, employeeID int) (*FiscalYear, error) {
	if err := ValidateStartMonth(in.FiscalYearStartMonth); err != nil {
		return nil, err
	}
	if in.BasePeriodStart == nil {
		return nil, ClientError{Msg: "basePeriodStart is required."}
	}
	base := FirstOfMonth(*in.BasePeriodStart)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin fiscal calendar setup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cal, err := lockCalendar(ctx, tx)
	if err != nil {
		return nil, err
	}
	if cal.Configured {
		return nil, conflict(ErrAlreadyConfigured,
			"The fiscal calendar has already been configured and cannot be changed.")
	}

	// Write the configuration BEFORE generating, so generateYear's history rows
	// and loadStates' IsBeforeBase computation both see the base period.
	if _, err := tx.Exec(ctx, `
		UPDATE accounting_settings
		SET fiscal_year_start_month = $1,
		    base_period_start = $2,
		    accounting_calendar_configured_at = CURRENT_TIMESTAMP,
		    accounting_settings_updated_at = CURRENT_TIMESTAMP,
		    accounting_settings_updated_by = $3
		WHERE accounting_settings_id = 1`,
		in.FiscalYearStartMonth, base, nullableInt(employeeID)); err != nil {
		return nil, fmt.Errorf("write fiscal calendar settings: %w", err)
	}

	fy, err := generateYear(ctx, tx,
		FiscalYearStart(base, in.FiscalYearStartMonth), &base, actionBaseSetup, employeeID)
	if err != nil {
		return nil, err
	}

	// Months before the base period were created closed, so the legacy column
	// must pick them up immediately — otherwise a tenant could post into them
	// through journal's fallback path until the first real close ran.
	if _, err := syncBooksClosedThrough(ctx, tx, employeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit fiscal calendar setup: %w", err)
	}
	return fy, nil
}
