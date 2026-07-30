package chartofaccounts

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// slotSelect is the shared projection for default mapping slots. The account
// join is LEFT so an unpointed slot still returns a row.
const slotSelect = `
	SELECT m.slot_key, m.slot_label, m.slot_description, a.coa_account_uuid,
	       COALESCE(a.coa_account_code,''), COALESCE(a.coa_account_name,''),
	       m.slot_is_system, m.slot_sort_order, m.slot_updated_at
	FROM coa_default_mapping m
	LEFT JOIN coa_account a
	       ON a.coa_account_id = m.coa_account_id AND a.coa_account_deleted_at IS NULL`

// scanSlot reads one row of the slotSelect projection.
func scanSlot(row pgx.Row) (*DefaultSlot, error) {
	var s DefaultSlot
	if err := row.Scan(&s.Key, &s.Label, &s.Description, &s.AccountID,
		&s.AccountCode, &s.AccountName, &s.IsSystem, &s.SortOrder, &s.UpdatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// Slots returns all default mapping slots in display order.
func Slots(ctx context.Context, pool *pgxpool.Pool) ([]DefaultSlot, error) {
	rows, err := pool.Query(ctx, slotSelect+` ORDER BY m.slot_sort_order, m.slot_key`)
	if err != nil {
		return nil, fmt.Errorf("list default slots: %w", err)
	}
	defer rows.Close()

	var out []DefaultSlot
	for rows.Next() {
		s, err := scanSlot(rows)
		if err != nil {
			return nil, fmt.Errorf("scan default slot: %w", err)
		}
		out = append(out, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate default slots: %w", err)
	}
	return out, nil
}

// RepointSlot points a slot at a different account, or clears it when
// accountUUID is empty.
//
// The target must be postable, active and live. That rule cannot be a foreign
// key -- Postgres cannot express a predicate on the referenced row -- so it is
// enforced here. This is the other half of guardRetire: the store refuses to
// point a slot at a disqualified account, and refuses to disqualify an account
// a slot points at (AD-7).
func RepointSlot(ctx context.Context, pool *pgxpool.Pool, slotKey, accountUUID string, employeeID int) (*DefaultSlot, error) {
	if accountUUID != "" && !validAccountUUID(accountUUID) {
		return nil, ClientError{Msg: fmt.Sprintf("%q is not a valid account id.", accountUUID)}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin repoint slot: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		prevID   *int
		prevCode string
	)
	err = tx.QueryRow(ctx, `
		SELECT m.coa_account_id, COALESCE(a.coa_account_code,'')
		FROM coa_default_mapping m
		LEFT JOIN coa_account a ON a.coa_account_id = m.coa_account_id
		WHERE m.slot_key = $1
		FOR UPDATE OF m`, slotKey).Scan(&prevID, &prevCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load default slot: %w", err)
	}

	var (
		nextID   *int
		nextCode string
	)
	if accountUUID != "" {
		var (
			id               int
			code, name       string
			postable, active bool
		)
		// FOR UPDATE is load-bearing, not defensive. Every retire path locks
		// this same coa_account row before calling guardRetire, and guardRetire
		// reads coa_default_mapping unlocked. Taking the account lock here makes
		// the account row the one serialization point between the two halves of
		// AD-7: whichever transaction gets it second re-reads under READ
		// COMMITTED and sees the other's committed effect, so it rejects instead
		// of stranding a slot on a retired account. Without it both commit and
		// the invariant breaks silently. See the MUST-FIX note above; do NOT
		// balance this by locking in BlockingSlots -- that inverts the lock
		// order and deadlocks.
		err := tx.QueryRow(ctx, `
			SELECT coa_account_id, coa_account_code, coa_account_name,
			       coa_account_is_postable, coa_account_is_active
			FROM coa_account
			WHERE coa_account_uuid = $1 AND coa_account_deleted_at IS NULL
			FOR UPDATE`, accountUUID).
			Scan(&id, &code, &name, &postable, &active)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ClientError{Msg: "The account does not exist."}
		}
		if err != nil {
			return nil, fmt.Errorf("load slot target account: %w", err)
		}
		if !postable {
			return nil, ConflictError{Msg: fmt.Sprintf(
				"Account %s %s is a header account and cannot be used as a default.", code, name)}
		}
		if !active {
			return nil, ConflictError{Msg: fmt.Sprintf(
				"Account %s %s is inactive and cannot be used as a default.", code, name)}
		}
		nextID, nextCode = &id, code
	}

	if _, err := tx.Exec(ctx, `
		UPDATE coa_default_mapping
		SET coa_account_id = $2, slot_updated_at = CURRENT_TIMESTAMP, slot_updated_by = $3
		WHERE slot_key = $1`, slotKey, nextID, nullableInt(employeeID)); err != nil {
		return nil, fmt.Errorf("repoint default slot: %w", err)
	}

	if err := appendHistory(ctx, tx, historyRow{
		AccountID: nextID, SlotKey: slotKey, Action: actionRepointSlot,
		Field: "coa_account_id", OldValue: prevCode, NewValue: nextCode,
		EmployeeID: employeeID,
	}); err != nil {
		return nil, err
	}

	slot, err := scanSlot(tx.QueryRow(ctx, slotSelect+` WHERE m.slot_key = $1`, slotKey))
	if err != nil {
		return nil, fmt.Errorf("read back default slot: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit repoint slot: %w", err)
	}
	return slot, nil
}
