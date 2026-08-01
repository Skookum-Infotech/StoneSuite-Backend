package accountingperiod

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// periodsPerQuarter mirrors QuartersFor's grouping: 4 quarters of 3 months
// each in every fiscal year. Derived from PeriodsPerYear rather than a bare
// 3, so the two constants cannot silently disagree.
const periodsPerQuarter = PeriodsPerYear / 4

// generateYear inserts one fiscal_year row, its four fiscal_quarter rows, and
// its twelve accounting_period rows, inside the caller's transaction.
//
// base is the go-live boundary. Periods ending before it are created closed —
// they stand for books closed in whatever system the tenant used before
// StoneSuite — and the period containing it is flagged is_base_period. A nil
// base means no boundary applies and every period is created open. A quarter
// is created closed under the identical rule, applied to its own three-month
// span, and every period's three sub-ledger locks (AP/AR/GL) are created at
// the SAME value as its own status -- this is what keeps the locks in
// lock-step with accounting_period_status for every period this function
// creates, the backward-compatibility property the whole lock design depends
// on (see spec §2.1).
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

	months := MonthsFor(start)
	quarterSpans := QuartersFor(months, fy.Name)
	quarterIDs := make([]int, len(quarterSpans))
	for i, span := range quarterSpans {
		status := StatusOpen
		if base != nil && span.End.Before(*base) {
			status = StatusClosed
		}

		var (
			qID int
			q   = Quarter{
				FiscalYearID: fy.ID, Number: span.Number, Name: span.Name,
				Start: span.Start, End: span.End, Status: status,
			}
		)
		err := tx.QueryRow(ctx, `
			INSERT INTO fiscal_quarter (
				fiscal_year_id, quarter_number, quarter_name,
				quarter_start, quarter_end, fiscal_quarter_status,
				fiscal_quarter_created_by, fiscal_quarter_updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$7)
			RETURNING fiscal_quarter_id, fiscal_quarter_uuid,
			          fiscal_quarter_created_at, fiscal_quarter_updated_at`,
			fyID, span.Number, span.Name, span.Start, span.End, status, nullableInt(employeeID),
		).Scan(&qID, &q.ID, &q.CreatedAt, &q.UpdatedAt)
		if isUniqueViolation(err) {
			return nil, conflict(ErrAlreadyConfigured, fmt.Sprintf(
				"A fiscal quarter already covers %s.", span.Name))
		}
		if err != nil {
			return nil, fmt.Errorf("insert fiscal quarter %s: %w", span.Name, err)
		}
		quarterIDs[i] = qID
		fy.Quarters = append(fy.Quarters, q)
	}

	for _, span := range months {
		status := StatusOpen
		var closedAt any
		if base != nil && span.End.Before(*base) {
			status = StatusClosed
			closedAt = time.Now().UTC()
		}
		isBase := base != nil && span.Start.Equal(*base)
		quarterIdx := (span.Number - 1) / periodsPerQuarter
		quarterID := quarterIDs[quarterIdx]

		var (
			periodID int
			p        = Period{
				FiscalYearID: fy.ID, FiscalYearName: fy.Name,
				Name: span.Name, Number: span.Number,
				Start: span.Start, End: span.End,
				Status: status, IsBasePeriod: isBase,
				APLockStatus: status, ARLockStatus: status, GLLockStatus: status,
				QuarterID:   fy.Quarters[quarterIdx].ID,
				QuarterName: fy.Quarters[quarterIdx].Name,
			}
		)
		err := tx.QueryRow(ctx, `
			INSERT INTO accounting_period (
				fiscal_year_id, accounting_period_name, period_number,
				period_start, period_end, accounting_period_status, is_base_period,
				accounting_period_closed_at, accounting_period_closed_by,
				fiscal_quarter_id, ap_lock_status, ar_lock_status, gl_lock_status,
				accounting_period_created_by, accounting_period_updated_by)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$6,$6,$6,$9,$9)
			RETURNING accounting_period_id, accounting_period_uuid,
			          accounting_period_closed_at,
			          accounting_period_created_at, accounting_period_updated_at`,
			fyID, span.Name, span.Number, span.Start, span.End, status, isBase,
			closedAt, nullableInt(employeeID), quarterID,
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

// maxGenerateYears is a bare sanity cap on a single GenerateFiscalYear call's
// range -- not a requirement from the design, just validation against an
// unbounded request size (consistent with "handlers must validate input").
const maxGenerateYears = 20

// GenerateFiscalYear generates one or more contiguous fiscal years' twelve
// periods each, all inside one transaction.
//
// Generation is forward-only and contiguous. A gap in the calendar would make
// "the contiguous closed prefix" — the thing books_closed_through reports —
// meaningless, and a backdated year is not a need the base period leaves open:
// Setup already generates the whole fiscal year containing the go-live month,
// with the months before it closed.
//
// in.StartYear, when nonzero, is a confirmation rather than a choice: it must
// match the next contiguous year, which makes an accidental double-generation
// a clear error instead of a silent second year. The range to generate is
// then chosen by at most one of in.EndYear (an explicit end year) or in.Years
// (a count of contiguous years) -- specifying both is a 400. Either way, any
// failure partway through — a duplicate, an overlap — rolls back every year
// already generated in this call via the deferred tx.Rollback, not just the
// one that failed.
func GenerateFiscalYear(ctx context.Context, pool *pgxpool.Pool, in GenerateInput, employeeID int) (*GenerateResult, error) {
	if in.EndYear != 0 && in.Years != 0 {
		return nil, ClientError{Msg: "specify only one of endYear or years, not both."}
	}
	if in.Years < 0 {
		return nil, ClientError{Msg: fmt.Sprintf("years must not be negative, got %d.", in.Years)}
	}
	if in.EndYear != 0 && in.StartYear == 0 {
		return nil, ClientError{Msg: "startYear is required when endYear is given."}
	}
	if in.EndYear != 0 && in.EndYear < in.StartYear {
		return nil, ClientError{Msg: "endYear cannot be before startYear."}
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

	// Resolve the range: both EndYear and Years zero means "just the next
	// year" (today's unchanged default); StartYear alone means "just that
	// year" (today's unchanged confirmation behaviour); EndYear or Years
	// extends it to a range (validated above as mutually exclusive).
	endYear := start.Year()
	switch {
	case in.EndYear != 0:
		endYear = in.EndYear
	case in.Years != 0:
		endYear = start.Year() + in.Years - 1
	case in.StartYear != 0:
		endYear = in.StartYear
	}
	count := endYear - start.Year() + 1
	if count > maxGenerateYears {
		return nil, ClientError{Msg: fmt.Sprintf(
			"cannot generate more than %d fiscal years in one call.", maxGenerateYears)}
	}

	years := make([]FiscalYear, 0, count)
	cursor := start
	for i := 0; i < count; i++ {
		fy, err := generateYear(ctx, tx, cursor, cal.BasePeriodStart, actionGenerate, employeeID)
		if err != nil {
			return nil, err
		}
		years = append(years, *fy)
		cursor = FirstOfMonth(fy.End.AddDate(0, 0, 1))
	}

	if _, err := syncBooksClosedThrough(ctx, tx, employeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit generate fiscal year: %w", err)
	}
	return &GenerateResult{
		FiscalYears:          years,
		FiscalYearStartMonth: cal.FiscalYearStartMonth,
		FiscalYearEndMonth:   FiscalYearEndMonth(cal.FiscalYearStartMonth),
	}, nil
}

// errNoSettingsRow guards against a tenant database whose accounting_settings
// singleton is missing. It is seeded ON CONFLICT DO NOTHING at every boot, so
// this is a corruption signal, not a normal state.
var errNoSettingsRow = errors.New("accounting settings row is missing")
