package accountingperiod

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Close closes one or more accounting periods.
//
// A single-period close is the one-element case of this call — there is no
// separate code path, so the sequencing rules cannot diverge between them.
func Close(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*StatusChangeResult, error) {
	return changeStatus(ctx, pool, ids, note, employeeID, true)
}

// Reopen reopens one or more accounting periods.
func Reopen(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*StatusChangeResult, error) {
	return changeStatus(ctx, pool, ids, note, employeeID, false)
}

// changeStatus applies a whole batch inside ONE transaction, all-or-nothing.
//
// Partial application is precisely the inconsistent state the sequencing rules
// exist to prevent: a batch that closed January and February and then failed on
// March would leave the books in a shape no rule would have permitted anyone to
// request. So the plan is validated in full first, against state read through
// the transaction, and any failure rolls the whole batch back.
func changeStatus(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int, closing bool) (*StatusChangeResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin accounting period status change: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock every period row for the duration. Without this, two concurrent
	// closes could each read a state in which their own move is legal, and both
	// commit — leaving a hole in the closed prefix that neither request could
	// have asked for on its own. The ORDER BY makes the lock order total, so
	// concurrent batches queue instead of deadlocking.
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM accounting_period ORDER BY period_start FOR UPDATE`); err != nil {
		return nil, fmt.Errorf("lock accounting periods: %w", err)
	}

	states, err := loadStates(ctx, tx)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 {
		return nil, ErrNotConfigured
	}

	var ordered []PeriodState
	if closing {
		ordered, err = PlanClose(ids, states)
	} else {
		ordered, err = PlanReopen(ids, states)
	}
	if err != nil {
		return nil, err
	}

	target, action := StatusOpen, actionReopen
	if closing {
		target, action = StatusClosed, actionClose
	}

	changed := make([]string, 0, len(ordered))
	for _, p := range ordered {
		if err := applyStatus(ctx, tx, p, target, action, note, employeeID); err != nil {
			return nil, err
		}
		changed = append(changed, p.ID)
	}

	if err := syncFiscalYearStatus(ctx, tx, changed); err != nil {
		return nil, err
	}
	if err := syncQuarterStatus(ctx, tx, changed); err != nil {
		return nil, err
	}
	through, err := syncBooksClosedThrough(ctx, tx, employeeID)
	if err != nil {
		return nil, err
	}

	periods, err := readBack(ctx, tx, changed)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit accounting period status change: %w", err)
	}
	return &StatusChangeResult{Periods: periods, BooksClosedThrough: through}, nil
}

// applyStatus writes one period's new status and its history row.
//
// closed_at is set with the status and cleared with it, which is what
// chk_ap_closed_pair enforces: the pairing is with the STATUS, not with
// closed_by, because the actor may legitimately be unresolved.
//
// The three sub-ledger locks are set to the SAME target in the same
// statement, which is what keeps them in lock-step with the derived overall
// status for every caller who only ever uses whole-period Close/Reopen and
// never the granular per-lock endpoints (store_locks.go) -- the backward
// compatibility property the whole lock design depends on (see spec §2.1).
func applyStatus(ctx context.Context, tx pgx.Tx, p PeriodState, target, action, note string, employeeID int) error {
	var (
		closedAt *time.Time
		closedBy any
	)
	if target == StatusClosed {
		now := time.Now().UTC()
		closedAt, closedBy = &now, nullableInt(employeeID)
	}

	var periodID int
	err := tx.QueryRow(ctx, `
		UPDATE accounting_period
		SET accounting_period_status = $2,
		    ap_lock_status = $2, ar_lock_status = $2, gl_lock_status = $2,
		    accounting_period_closed_at = $3,
		    accounting_period_closed_by = $4,
		    accounting_period_updated_at = CURRENT_TIMESTAMP,
		    accounting_period_updated_by = $5
		WHERE accounting_period_uuid = $1
		RETURNING accounting_period_id`,
		p.ID, target, closedAt, closedBy, nullableInt(employeeID),
	).Scan(&periodID)
	if err != nil {
		return fmt.Errorf("set accounting period %s to %s: %w", p.Name, target, err)
	}
	return writeHistory(ctx, tx, periodID, action, p.Status, target, note, employeeID)
}

// readBack loads the changed periods in chronological order for the response.
func readBack(ctx context.Context, tx pgx.Tx, uuids []string) ([]Period, error) {
	rows, err := tx.Query(ctx, periodSelect+`
		WHERE ap.accounting_period_uuid = ANY($1)
		ORDER BY ap.period_start`, uuids)
	if err != nil {
		return nil, fmt.Errorf("read back accounting periods: %w", err)
	}
	defer rows.Close()

	out := []Period{}
	for rows.Next() {
		p, err := scanPeriod(rows)
		if err != nil {
			return nil, fmt.Errorf("scan changed accounting period: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate changed accounting periods: %w", err)
	}
	return out, nil
}
