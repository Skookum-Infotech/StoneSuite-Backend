package accountingperiod

import (
	"context"
	"fmt"
	"time"
)

// syncBooksClosedThrough recomputes accounting_settings.books_closed_through
// from the current period table and writes it back, returning the new value.
//
// This is what keeps the legacy single-date period concept truthful after this
// module takes over. journal falls back to that column for tenants with no
// fiscal calendar, and anything else still reading it — reports, exports —
// keeps getting a correct answer instead of a frozen one.
//
// It must be called inside the same transaction as every status change, so a
// rolled-back close cannot leave the column claiming months are closed.
func syncBooksClosedThrough(ctx context.Context, q querier, employeeID int) (*time.Time, error) {
	states, err := loadStates(ctx, q)
	if err != nil {
		return nil, err
	}
	through := ClosedThrough(states)

	if _, err := q.Exec(ctx, `
		UPDATE accounting_settings
		SET books_closed_through = $1,
		    accounting_settings_updated_at = CURRENT_TIMESTAMP,
		    accounting_settings_updated_by = $2
		WHERE accounting_settings_id = 1`, through, nullableInt(employeeID)); err != nil {
		return nil, fmt.Errorf("sync books closed through: %w", err)
	}
	return through, nil
}

// syncFiscalYearStatus recomputes the derived status of every fiscal year that
// owns one of the given periods: closed only when all twelve of its periods
// are closed.
//
// Deriving it — rather than letting a caller set it — is what makes it
// impossible for a year to claim "closed" while a period under it is open.
func syncFiscalYearStatus(ctx context.Context, q querier, periodUUIDs []string) error {
	if len(periodUUIDs) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `
		UPDATE fiscal_year fy
		SET fiscal_year_status = CASE
		        WHEN EXISTS (
		            SELECT 1 FROM accounting_period ap
		            WHERE ap.fiscal_year_id = fy.fiscal_year_id
		              AND ap.accounting_period_status = $2
		        ) OR NOT EXISTS (
		            SELECT 1 FROM accounting_period ap
		            WHERE ap.fiscal_year_id = fy.fiscal_year_id
		        ) THEN $2
		        ELSE $3
		    END,
		    fiscal_year_updated_at = CURRENT_TIMESTAMP
		WHERE fy.fiscal_year_id IN (
		    SELECT ap.fiscal_year_id FROM accounting_period ap
		    WHERE ap.accounting_period_uuid = ANY($1)
		)`, periodUUIDs, StatusOpen, StatusClosed); err != nil {
		return fmt.Errorf("sync fiscal year status: %w", err)
	}
	return nil
}

// syncQuarterStatus recomputes the derived status of every fiscal quarter
// that owns one of the given periods: closed only when all three of its
// periods are closed. Same derivation shape as syncFiscalYearStatus, scoped
// by fiscal_quarter_id instead of fiscal_year_id.
//
// A period generated before quarters existed carries a NULL
// fiscal_quarter_id (quarters are not backfilled onto already-generated
// fiscal years) and is excluded by construction: it can never appear as the
// inner query's fiscal_quarter_id, so no quarter row is touched for it.
//
// Every caller of syncFiscalYearStatus -- changeStatus's whole-period
// Close/Reopen and changeLock's six granular lock endpoints -- calls this
// immediately alongside it, so a quarter's derived status can never drift
// from the periods under it.
func syncQuarterStatus(ctx context.Context, q querier, periodUUIDs []string) error {
	if len(periodUUIDs) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `
		UPDATE fiscal_quarter fq
		SET fiscal_quarter_status = CASE
		        WHEN EXISTS (
		            SELECT 1 FROM accounting_period ap
		            WHERE ap.fiscal_quarter_id = fq.fiscal_quarter_id
		              AND ap.accounting_period_status = $2
		        ) OR NOT EXISTS (
		            SELECT 1 FROM accounting_period ap
		            WHERE ap.fiscal_quarter_id = fq.fiscal_quarter_id
		        ) THEN $2
		        ELSE $3
		    END,
		    fiscal_quarter_updated_at = CURRENT_TIMESTAMP
		WHERE fq.fiscal_quarter_id IN (
		    SELECT ap.fiscal_quarter_id FROM accounting_period ap
		    WHERE ap.accounting_period_uuid = ANY($1)
		      AND ap.fiscal_quarter_id IS NOT NULL
		)`, periodUUIDs, StatusOpen, StatusClosed); err != nil {
		return fmt.Errorf("sync fiscal quarter status: %w", err)
	}
	return nil
}
