package chartofaccounts

import (
	"context"
	"fmt"
)

// History action verbs, matching chk_coa_history_action in tenant/schema.sql.
const (
	actionCreate      = "create"
	actionUpdate      = "update"
	actionDelete      = "delete"
	actionActivate    = "activate"
	actionDeactivate  = "deactivate"
	actionShow        = "show"
	actionHide        = "hide"
	actionRepointSlot = "repoint_slot"
)

// redactedValue stands in for a bank account number in the audit trail. The
// number itself is never written to history or to any log (AD-10) -- only the
// fact that it changed.
const redactedValue = "[redacted]"

// historyRow is one audited change. Exactly one of AccountID or SlotKey is set.
type historyRow struct {
	AccountID  *int
	SlotKey    string
	Action     string
	Field      string
	OldValue   string
	NewValue   string
	EmployeeID int
}

// appendHistory writes one audit row. It takes a rowQuerier so callers inside
// a transaction record their history in that same transaction -- a rolled-back
// mutation must not leave an audit row claiming it happened.
func appendHistory(ctx context.Context, q rowQuerier, h historyRow) error {
	if h.Field == BankAccountNumberKey {
		h.OldValue, h.NewValue = redactedValue, redactedValue
	}
	var slot any
	if h.SlotKey != "" {
		slot = h.SlotKey
	}
	_, err := q.Exec(ctx, `
		INSERT INTO coa_account_history
			(coa_account_id, slot_key, history_action, history_field,
			 history_old_value, history_new_value, history_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		h.AccountID, slot, h.Action, h.Field, h.OldValue, h.NewValue,
		nullableInt(h.EmployeeID))
	if err != nil {
		return fmt.Errorf("append account history: %w", err)
	}
	return nil
}

// History returns the audit trail for one account, newest first.
func History(ctx context.Context, pool rowQuerier, uuid string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT h.coa_account_history_id, a.coa_account_uuid, COALESCE(h.slot_key,''),
		       h.history_action, h.history_field, h.history_old_value,
		       h.history_new_value, h.history_at, h.history_by
		FROM coa_account_history h
		JOIN coa_account a ON a.coa_account_id = h.coa_account_id
		WHERE a.coa_account_uuid = $1
		ORDER BY h.history_at DESC, h.coa_account_history_id DESC
		LIMIT $2`, uuid, limit)
	if err != nil {
		return nil, fmt.Errorf("list account history: %w", err)
	}
	defer rows.Close()

	var out []HistoryEntry
	for rows.Next() {
		var (
			e         HistoryEntry
			accountID string
		)
		if err := rows.Scan(&e.ID, &accountID, &e.SlotKey, &e.Action, &e.Field,
			&e.OldValue, &e.NewValue, &e.At, &e.By); err != nil {
			return nil, fmt.Errorf("scan history entry: %w", err)
		}
		e.AccountID = &accountID
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate history: %w", err)
	}
	return out, nil
}
