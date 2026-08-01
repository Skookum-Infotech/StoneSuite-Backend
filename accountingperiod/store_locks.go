package accountingperiod

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// store_locks.go implements the six granular sub-ledger lock endpoints --
// the second of the two ways a period's lock state changes (see
// store_status.go for the first: whole-period Close/Reopen, which sets all
// three locks to the same target in one statement).
//
// Each of the three sub-ledger locks -- AP, AR, GL -- is independent: it is
// sequenced by the SAME chronological-order rule PlanClose/PlanReopen apply
// to the derived overall Status (see PlanCloseLock/PlanReopenLock in
// rules.go), but scoped to that one lock's own history. AP can lag GL by a
// period the way real close cycles do, but AP itself still cannot skip a
// month. After a granular change, accounting_period_status is RECOMPUTED
// from the three current lock values (never assumed) by
// recomputePeriodStatus -- the store-side mirror of how fiscal_year_status
// and fiscal_quarter_status are already derived from their periods, never
// set directly by a caller.

// column returns the fixed SQL column name for this lock. LockField is a
// closed, compile-time enum of exactly three values (see rules.go), never
// derived from request data, so building the UPDATE statement's SET clause
// with fmt.Sprintf using this value carries no injection surface.
func (f LockField) column() string {
	switch f {
	case LockAP:
		return "ap_lock_status"
	case LockAR:
		return "ar_lock_status"
	case LockGL:
		return "gl_lock_status"
	}
	return ""
}

// lockAction returns the accounting_period_history verb for closing this
// lock, matching the widened chk_ap_history_action in tenant/schema.sql.
func (f LockField) lockAction() string {
	switch f {
	case LockAP:
		return "ap_lock"
	case LockAR:
		return "ar_lock"
	case LockGL:
		return "gl_lock"
	}
	return ""
}

// unlockAction returns the accounting_period_history verb for opening this
// lock, matching the widened chk_ap_history_action in tenant/schema.sql.
func (f LockField) unlockAction() string {
	switch f {
	case LockAP:
		return "ap_unlock"
	case LockAR:
		return "ar_unlock"
	case LockGL:
		return "gl_unlock"
	}
	return ""
}

// changeLock applies a batch lock/unlock to one of a period's three
// independent sub-ledger locks, inside one transaction, all-or-nothing --
// mirroring changeStatus in store_status.go exactly, except the sequencing
// rule and the write are scoped to lock's own column rather than the derived
// overall Status.
func changeLock(ctx context.Context, pool *pgxpool.Pool, lock LockField, ids []string, note string, employeeID int, closing bool) (*StatusChangeResult, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin accounting period lock change: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock every period row for the duration -- same reasoning as
	// changeStatus: without this, two concurrent lock changes could each read
	// a state in which their own move is legal and both commit, leaving a
	// hole in that lock's closed prefix that neither request could have asked
	// for on its own. The ORDER BY makes the lock order total, so concurrent
	// batches queue instead of deadlocking.
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
		ordered, err = PlanCloseLock(ids, states, lock)
	} else {
		ordered, err = PlanReopenLock(ids, states, lock)
	}
	if err != nil {
		return nil, err
	}

	target, action := StatusOpen, lock.unlockAction()
	if closing {
		target, action = StatusClosed, lock.lockAction()
	}

	changed := make([]string, 0, len(ordered))
	periodIDs := make([]int, 0, len(ordered))
	for _, p := range ordered {
		periodID, err := applyLock(ctx, tx, p, lock, target, action, note, employeeID)
		if err != nil {
			return nil, err
		}
		changed = append(changed, p.ID)
		periodIDs = append(periodIDs, periodID)
	}

	// The overall status is never assumed -- it is recomputed from whatever
	// the three lock columns actually hold after this batch's writes.
	if err := recomputePeriodStatus(ctx, tx, periodIDs, employeeID); err != nil {
		return nil, err
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
		return nil, fmt.Errorf("commit accounting period lock change: %w", err)
	}
	return &StatusChangeResult{Periods: periods, BooksClosedThrough: through}, nil
}

// applyLock writes one period's new value for a single sub-ledger lock and
// its history row. Mirrors applyStatus in store_status.go, but touches only
// lock.column() (plus the updated_at/by pair) -- never
// accounting_period_status directly, which is recomputed afterward by
// recomputePeriodStatus from all three locks. Returns the period's int id,
// needed by the caller for that recompute.
func applyLock(ctx context.Context, tx pgx.Tx, p PeriodState, lock LockField, target, action, note string, employeeID int) (int, error) {
	var periodID int
	err := tx.QueryRow(ctx, fmt.Sprintf(`
		UPDATE accounting_period
		SET %s = $2,
		    accounting_period_updated_at = CURRENT_TIMESTAMP,
		    accounting_period_updated_by = $3
		WHERE accounting_period_uuid = $1
		RETURNING accounting_period_id`, lock.column()),
		p.ID, target, nullableInt(employeeID),
	).Scan(&periodID)
	if err != nil {
		return 0, fmt.Errorf("set accounting period %s %s to %s: %w", lock.label(), p.Name, target, err)
	}
	if err := writeHistory(ctx, tx, periodID, action, lock.get(p), target, note, employeeID); err != nil {
		return 0, err
	}
	return periodID, nil
}

// recomputePeriodStatus derives accounting_period_status -- and the paired
// closed_at/closed_by -- from the three lock columns: closed iff all three
// are closed. This is the store-side mirror of fiscal_year_status and
// fiscal_quarter_status already being derived from their periods: the
// overall period status is never written directly by a lock change, only
// recomputed from the locks that WERE written directly (applyLock).
//
// closed_at is preserved via COALESCE rather than stamped fresh, so closing
// the last of three already-closed-at-different-times locks does not move a
// timestamp a caller already read as "when this period closed" -- it is set
// once, the first time all three become closed, and cleared to NULL the
// moment any one of them opens again, satisfying chk_ap_closed_pair in both
// directions.
func recomputePeriodStatus(ctx context.Context, q querier, periodIDs []int, employeeID int) error {
	if len(periodIDs) == 0 {
		return nil
	}
	if _, err := q.Exec(ctx, `
		UPDATE accounting_period
		SET accounting_period_status = CASE
		        WHEN ap_lock_status = $2 AND ar_lock_status = $2 AND gl_lock_status = $2
		        THEN $2 ELSE $3 END,
		    accounting_period_closed_at = CASE
		        WHEN ap_lock_status = $2 AND ar_lock_status = $2 AND gl_lock_status = $2
		        THEN COALESCE(accounting_period_closed_at, CURRENT_TIMESTAMP)
		        ELSE NULL END,
		    accounting_period_closed_by = CASE
		        WHEN ap_lock_status = $2 AND ar_lock_status = $2 AND gl_lock_status = $2
		        THEN COALESCE(accounting_period_closed_by, $4)
		        ELSE NULL END,
		    accounting_period_updated_at = CURRENT_TIMESTAMP,
		    accounting_period_updated_by = $4
		WHERE accounting_period_id = ANY($1)`,
		periodIDs, StatusClosed, StatusOpen, nullableInt(employeeID)); err != nil {
		return fmt.Errorf("recompute accounting period status: %w", err)
	}
	return nil
}

// LockAPPeriods closes one or more periods' AP sub-ledger lock.
//
// Named with the "Periods" suffix -- unlike Close/Reopen's bare names --
// because LockField's own LockAP/LockAR/LockGL constants (rules.go) already
// occupy the shorter names in this package's identifier space.
func LockAPPeriods(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*StatusChangeResult, error) {
	return changeLock(ctx, pool, LockAP, ids, note, employeeID, true)
}

// UnlockAPPeriods reopens one or more periods' AP sub-ledger lock.
func UnlockAPPeriods(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*StatusChangeResult, error) {
	return changeLock(ctx, pool, LockAP, ids, note, employeeID, false)
}

// LockARPeriods closes one or more periods' AR sub-ledger lock.
func LockARPeriods(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*StatusChangeResult, error) {
	return changeLock(ctx, pool, LockAR, ids, note, employeeID, true)
}

// UnlockARPeriods reopens one or more periods' AR sub-ledger lock.
func UnlockARPeriods(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*StatusChangeResult, error) {
	return changeLock(ctx, pool, LockAR, ids, note, employeeID, false)
}

// LockGLPeriods closes one or more periods' GL sub-ledger lock -- the choke
// point journal.CheckPeriodOpen reads (see the design spec §2.2; wiring that
// read is a separate change, out of scope here).
func LockGLPeriods(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*StatusChangeResult, error) {
	return changeLock(ctx, pool, LockGL, ids, note, employeeID, true)
}

// UnlockGLPeriods reopens one or more periods' GL sub-ledger lock.
func UnlockGLPeriods(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*StatusChangeResult, error) {
	return changeLock(ctx, pool, LockGL, ids, note, employeeID, false)
}
