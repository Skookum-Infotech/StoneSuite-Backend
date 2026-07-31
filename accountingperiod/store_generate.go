package accountingperiod

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// generateYear inserts one fiscal_year row and its twelve accounting_period
// rows, inside the caller's transaction.
//
// base is the go-live boundary. Periods ending before it are created closed —
// they stand for books closed in whatever system the tenant used before
// StoneSuite — and the period containing it is flagged is_base_period. A nil
// base means no boundary applies and every period is created open.
func generateYear(ctx context.Context, tx pgx.Tx, start time.Time, base *time.Time, action string, employeeID int) (*FiscalYear, error) {
	start = FirstOfMonth(start)
	fy := FiscalYear{
		Name:   FiscalYearLabel(start),
		Start:  start,
		End:    FiscalYearEnd(start),
		Status: StatusOpen,
	}

	var fyID int
	err := tx.QueryRow(ctx, `
		INSERT INTO fiscal_year (fiscal_year_name, fiscal_year_start, fiscal_year_end,
			fiscal_year_status, fiscal_year_created_by, fiscal_year_updated_by)
		VALUES ($1,$2,$3,$4,$5,$5)
		RETURNING fiscal_year_id, fiscal_year_uuid, fiscal_year_created_at, fiscal_year_updated_at`,
		fy.Name, fy.Start, fy.End, StatusOpen, nullableInt(employeeID),
	).Scan(&fyID, &fy.ID, &fy.CreatedAt, &fy.UpdatedAt)
	if isUniqueViolation(err) {
		return nil, conflict(ErrAlreadyConfigured, fmt.Sprintf(
			"Fiscal year %s already exists.", fy.Name))
	}
	if err != nil {
		return nil, fmt.Errorf("insert fiscal year %s: %w", fy.Name, err)
	}

	for _, span := range MonthsFor(start) {
		status := StatusOpen
		var closedAt any
		if base != nil && span.End.Before(*base) {
			status = StatusClosed
			closedAt = time.Now().UTC()
		}
		isBase := base != nil && span.Start.Equal(*base)

		var (
			periodID int
			p        = Period{
				FiscalYearID: fy.ID, FiscalYearName: fy.Name,
				Name: span.Name, Number: span.Number,
				Start: span.Start, End: span.End,
				Status: status, IsBasePeriod: isBase,
			}
		)
		err := tx.QueryRow(ctx, `
			INSERT INTO accounting_period (
				fiscal_year_id, accounting_period_name, period_number,
				period_start, period_end, accounting_period_status, is_base_period,
				accounting_period_closed_at, accounting_period_closed_by,
				accounting_period_created_by, accounting_period_updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$9)
			RETURNING accounting_period_id, accounting_period_uuid,
			          accounting_period_closed_at,
			          accounting_period_created_at, accounting_period_updated_at`,
			fyID, span.Name, span.Number, span.Start, span.End, status, isBase,
			closedAt, nullableInt(employeeID),
		).Scan(&periodID, &p.ID, &p.ClosedAt, &p.CreatedAt, &p.UpdatedAt)
		if isUniqueViolation(err) {
			return nil, conflict(ErrAlreadyConfigured, fmt.Sprintf(
				"An accounting period already covers %s.", span.Name))
		}
		if err != nil {
			return nil, fmt.Errorf("insert accounting period %s: %w", span.Name, err)
		}

		if err := writeHistory(ctx, tx, periodID, action, "", status, "", employeeID); err != nil {
			return nil, err
		}
		fy.Periods = append(fy.Periods, p)
	}
	return &fy, nil
}

// nextFiscalYearStart returns the first day of the fiscal year that follows the
// latest one on record, and whether any fiscal year exists at all.
func nextFiscalYearStart(ctx context.Context, q querier) (time.Time, bool, error) {
	var latestEnd *time.Time
	if err := q.QueryRow(ctx,
		`SELECT MAX(fiscal_year_end) FROM fiscal_year`).Scan(&latestEnd); err != nil {
		return time.Time{}, false, fmt.Errorf("load latest fiscal year: %w", err)
	}
	if latestEnd == nil {
		return time.Time{}, false, nil
	}
	return FirstOfMonth(latestEnd.AddDate(0, 0, 1)), true, nil
}

// GenerateFiscalYear generates one or more contiguous fiscal years' twelve
// periods each, in a single transaction.
//
// Generation is forward-only and contiguous. A gap in the calendar would make
// "the contiguous closed prefix" — the thing books_closed_through reports —
// meaningless, and a backdated year is not a need the base period leaves open:
// Setup already generates the whole fiscal year containing the go-live month,
// with the months before it closed. Requesting several years does not relax
// this: they are generated back-to-back starting from the next contiguous
// year, never at an arbitrary chosen year.
//
// in.StartYear, when nonzero, is a confirmation rather than a choice: it must
// match the first generated year's start, which makes an accidental
// double-generation a clear error instead of a silent extra year. in.Years
// (default 1, capped at MaxGenerateYears) is genuinely a choice — how many
// contiguous years to generate in this one call — and the whole batch is one
// transaction: a failure on any year (e.g. it already exists) rolls back every
// year in the request rather than leaving a partial run.
func GenerateFiscalYear(ctx context.Context, pool *pgxpool.Pool, in GenerateInput, employeeID int) ([]*FiscalYear, error) {
	years := in.Years
	if years == 0 {
		years = 1
	}
	if err := ValidateGenerateYears(years); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin generate fiscal year: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cal, err := lockCalendar(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !cal.Configured {
		return nil, ErrNotConfigured
	}

	start, exists, err := nextFiscalYearStart(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !exists {
		// Setup always generates a year, so this means the calendar row and the
		// fiscal_year table disagree. Refuse rather than invent a start date.
		return nil, ErrNotConfigured
	}
	if in.StartYear != 0 && in.StartYear != start.Year() {
		return nil, ClientError{Msg: fmt.Sprintf(
			"The next fiscal year starts in %d, not %d.", start.Year(), in.StartYear)}
	}

	fys := make([]*FiscalYear, 0, years)
	for i := 0; i < years; i++ {
		fy, err := generateYear(ctx, tx, start, cal.BasePeriodStart, actionGenerate, employeeID)
		if err != nil {
			return nil, err
		}
		fys = append(fys, fy)
		// fy.End is always the last day of a month (FiscalYearEnd), so the day
		// after it is already the first of the next fiscal year's start month.
		start = fy.End.AddDate(0, 0, 1)
	}
	if _, err := syncBooksClosedThrough(ctx, tx, employeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit generate fiscal year: %w", err)
	}
	return fys, nil
}

// errNoSettingsRow guards against a tenant database whose accounting_settings
// singleton is missing. It is seeded ON CONFLICT DO NOTHING at every boot, so
// this is a corruption signal, not a normal state.
var errNoSettingsRow = errors.New("accounting settings row is missing")
