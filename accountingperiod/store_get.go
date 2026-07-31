package accountingperiod

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// querier is the subset of pgx behavior this package needs, declared
// consumer-side so a *pgxpool.Pool and a pgx.Tx are interchangeable. Reads
// inside a mutating transaction MUST use the tx, so they observe that
// transaction's own uncommitted effects.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// maxPeriodRows caps an unfiltered period listing at fifty fiscal years.
//
// This module deliberately does not route through the query/ filter engine:
// that engine exists for unbounded user records with RBAC scope narrowing, and
// a fiscal calendar is bounded reference data — twelve rows a year — with no
// owner to scope against. It is the same call chartofaccounts.Categories makes.
const maxPeriodRows = 600

const periodSelect = `
	SELECT ap.accounting_period_uuid, fy.fiscal_year_uuid, fy.fiscal_year_name,
	       ap.accounting_period_name, ap.period_number,
	       ap.period_start, ap.period_end,
	       ap.accounting_period_status, ap.is_base_period,
	       ap.accounting_period_closed_at,
	       ap.accounting_period_created_at, ap.accounting_period_updated_at
	FROM accounting_period ap
	JOIN fiscal_year fy ON fy.fiscal_year_id = ap.fiscal_year_id`

func scanPeriod(row pgx.Row) (*Period, error) {
	var p Period
	if err := row.Scan(&p.ID, &p.FiscalYearID, &p.FiscalYearName,
		&p.Name, &p.Number, &p.Start, &p.End,
		&p.Status, &p.IsBasePeriod, &p.ClosedAt,
		&p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

// Filters narrows a period listing. Both fields are optional.
type Filters struct {
	FiscalYear string // fiscal_year_name, e.g. "FY2026"
	Status     string // "open" | "closed"
}

// List returns periods in chronological order, oldest first.
func List(ctx context.Context, pool *pgxpool.Pool, f Filters) ([]Period, error) {
	var conds []string
	var args []any
	if f.FiscalYear != "" {
		args = append(args, f.FiscalYear)
		conds = append(conds, fmt.Sprintf("fy.fiscal_year_name = $%d", len(args)))
	}
	if f.Status != "" {
		if f.Status != StatusOpen && f.Status != StatusClosed {
			return nil, ClientError{Msg: `status must be "open" or "closed".`}
		}
		args = append(args, f.Status)
		conds = append(conds, fmt.Sprintf("ap.accounting_period_status = $%d", len(args)))
	}
	where := ""
	if len(conds) > 0 {
		where = " WHERE " + strings.Join(conds, " AND ")
	}
	args = append(args, maxPeriodRows)

	rows, err := pool.Query(ctx, periodSelect+where+
		fmt.Sprintf(" ORDER BY ap.period_start LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, fmt.Errorf("list accounting periods: %w", err)
	}
	defer rows.Close()

	out := []Period{}
	for rows.Next() {
		p, err := scanPeriod(rows)
		if err != nil {
			return nil, fmt.Errorf("scan accounting period: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounting periods: %w", err)
	}
	return out, nil
}

// Get loads one period by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Period, error) {
	p, err := scanPeriod(pool.QueryRow(ctx, periodSelect+
		` WHERE ap.accounting_period_uuid = $1`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get accounting period: %w", err)
	}
	return p, nil
}

// ForDate returns the period covering d, or ErrNotFound when none does.
func ForDate(ctx context.Context, pool *pgxpool.Pool, d time.Time) (*Period, error) {
	p, err := scanPeriod(pool.QueryRow(ctx, periodSelect+
		` WHERE $1::date BETWEEN ap.period_start AND ap.period_end`, d))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get accounting period for date: %w", err)
	}
	return p, nil
}

// Current returns the period covering today.
func Current(ctx context.Context, pool *pgxpool.Pool) (*Period, error) {
	return ForDate(ctx, pool, time.Now().UTC())
}

// ListFiscalYears returns every generated fiscal year, oldest first.
func ListFiscalYears(ctx context.Context, pool *pgxpool.Pool) ([]FiscalYear, error) {
	rows, err := pool.Query(ctx, `
		SELECT fiscal_year_uuid, fiscal_year_name, fiscal_year_start, fiscal_year_end,
		       fiscal_year_status, fiscal_year_created_at, fiscal_year_updated_at
		FROM fiscal_year ORDER BY fiscal_year_start`)
	if err != nil {
		return nil, fmt.Errorf("list fiscal years: %w", err)
	}
	defer rows.Close()

	out := []FiscalYear{}
	for rows.Next() {
		var fy FiscalYear
		if err := rows.Scan(&fy.ID, &fy.Name, &fy.Start, &fy.End,
			&fy.Status, &fy.CreatedAt, &fy.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan fiscal year: %w", err)
		}
		out = append(out, fy)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fiscal years: %w", err)
	}
	return out, nil
}

// loadStates reads every period as the minimal shape the sequencing rules
// need. It takes a querier so a mutating transaction reads its own effects.
//
// IsBeforeBase is computed here, against accounting_settings.base_period_start,
// so the rules stay free of database concerns.
func loadStates(ctx context.Context, q querier) ([]PeriodState, error) {
	rows, err := q.Query(ctx, `
		SELECT ap.accounting_period_uuid, ap.accounting_period_name,
		       ap.period_start, ap.period_end, ap.accounting_period_status,
		       COALESCE(ap.period_end < s.base_period_start, FALSE)
		FROM accounting_period ap
		CROSS JOIN accounting_settings s
		WHERE s.accounting_settings_id = 1
		ORDER BY ap.period_start`)
	if err != nil {
		return nil, fmt.Errorf("load accounting period states: %w", err)
	}
	defer rows.Close()

	var out []PeriodState
	for rows.Next() {
		var p PeriodState
		if err := rows.Scan(&p.ID, &p.Name, &p.Start, &p.End, &p.Status, &p.IsBeforeBase); err != nil {
			return nil, fmt.Errorf("scan accounting period state: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounting period states: %w", err)
	}
	return out, nil
}
