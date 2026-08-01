package accountingperiod

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// maxHistoryRows caps a history page.
const maxHistoryRows = 200

// writeHistory appends one row to accounting_period_history, inside whatever
// transaction the caller is running so a rolled-back close leaves no trail
// claiming it happened.
//
// Unlike cashtransfer.writeHistory this returns its error rather than
// swallowing it. Closing an accounting period is an auditable act: if the
// trail cannot be written, the close must not stand. That is the
// chartofaccounts.appendHistory posture, and the right one here.
func writeHistory(ctx context.Context, q querier, periodID int, action, fromStatus, toStatus, note string, employeeID int) error {
	var from, to any
	if fromStatus != "" {
		from = fromStatus
	}
	if toStatus != "" {
		to = toStatus
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO accounting_period_history
			(accounting_period_id, history_action, history_from_status,
			 history_to_status, history_note, history_by)
		VALUES ($1,$2,$3,$4,$5,$6)`,
		periodID, action, from, to, note, nullableInt(employeeID)); err != nil {
		return fmt.Errorf("append accounting period history: %w", err)
	}
	return nil
}

// History returns one period's audit trail, newest first.
func History(ctx context.Context, pool *pgxpool.Pool, uuid string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > maxHistoryRows {
		limit = maxHistoryRows / 2
	}
	// Confirm the period exists so an unknown uuid is a 404 rather than an
	// empty trail that looks like a period with no history.
	if _, err := Get(ctx, pool, uuid); err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx, `
		SELECT h.accounting_period_history_id, ap.accounting_period_uuid,
		       h.history_action, COALESCE(h.history_from_status,''),
		       COALESCE(h.history_to_status,''), h.history_note,
		       h.history_by, h.history_at
		FROM accounting_period_history h
		JOIN accounting_period ap ON ap.accounting_period_id = h.accounting_period_id
		WHERE ap.accounting_period_uuid = $1
		ORDER BY h.history_at DESC, h.accounting_period_history_id DESC
		LIMIT $2`, uuid, limit)
	if err != nil {
		return nil, fmt.Errorf("list accounting period history: %w", err)
	}
	defer rows.Close()

	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.ID, &e.PeriodID, &e.Action, &e.FromStatus,
			&e.ToStatus, &e.Note, &e.By, &e.At); err != nil {
			return nil, fmt.Errorf("scan accounting period history: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounting period history: %w", err)
	}
	return out, nil
}
