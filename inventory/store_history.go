package inventory

// store_history.go — the item audit trail.
//
// Distinct from inventory_unit_history, which tracks physical pieces (bin
// moves, cuts, scrap). This one tracks catalogue edits.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ItemHistoryEntry is one recorded change to an item.
type ItemHistoryEntry struct {
	Action    string `json:"action"`
	Field     string `json:"field"`
	OldValue  string `json:"oldValue"`
	NewValue  string `json:"newValue"`
	At        string `json:"at"`
	ByName    string `json:"byName"`
	ByLoginID *int   `json:"byId,omitempty"`
}

// writeItemHistory appends one row to inventory_item_history. It takes a pgx.Tx
// directly rather than an interface because every caller already holds one —
// history must never land in a different transaction from the change it
// describes, or a rolled-back edit leaves a phantom audit row.
func writeItemHistory(ctx context.Context, tx pgx.Tx, itemID int, action, field, oldVal, newVal string, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_item_history (
			inventory_item_id, history_action, history_field,
			history_old_value, history_new_value, history_by
		) VALUES ($1,$2,$3,$4,$5,$6)`,
		itemID, action, field, oldVal, newVal, nullableInt(actorEmployeeID),
	); err != nil {
		return fmt.Errorf("write inventory item history: %w", err)
	}
	return nil
}

// ItemHistory returns the audit trail for one item, newest first.
func ItemHistory(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]ItemHistoryEntry, error) {
	id, err := itemIDByUUID(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT h.history_action, h.history_field, h.history_old_value, h.history_new_value,
		       to_char(h.history_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
		       -- history_by is nullable and the name columns are NOT NULL DEFAULT '',
		       -- so an unmatched LEFT JOIN and a nameless employee both have to
		       -- collapse to 'System' rather than to an empty string.
		       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
		                            COALESCE(e.employee_last_name,'')), ''), 'System'),
		       h.history_by
		FROM inventory_item_history h
		LEFT JOIN employee e ON e.employee_id = h.history_by
		WHERE h.inventory_item_id = $1
		ORDER BY h.history_at DESC, h.inventory_item_history_id DESC
		LIMIT 200`, id)
	if err != nil {
		return nil, fmt.Errorf("load inventory item history: %w", err)
	}
	defer rows.Close()

	out := []ItemHistoryEntry{}
	for rows.Next() {
		var e ItemHistoryEntry
		if err := rows.Scan(&e.Action, &e.Field, &e.OldValue, &e.NewValue, &e.At, &e.ByName, &e.ByLoginID); err != nil {
			return nil, fmt.Errorf("scan inventory item history: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load inventory item history: %w", err)
	}
	return out, nil
}
