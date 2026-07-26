package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxBulkAccounts caps one bulk request. The tenant ships with 127 accounts,
// so this comfortably covers "select all" while bounding the transaction.
const maxBulkAccounts = 500

// BulkUpdate toggles is_active / is_visible across many accounts in ONE
// transaction. Any single failure rolls the whole batch back: a visibility
// change applied to half a chart of accounts is worse than one applied to none.
//
// Each account runs the same guardRetire check as a single update (AD-7).
func BulkUpdate(ctx context.Context, pool *pgxpool.Pool, in BulkInput, employeeID int) ([]BulkResult, error) {
	if len(in.UUIDs) == 0 {
		return nil, ClientError{Msg: "Select at least one account."}
	}
	if len(in.UUIDs) > maxBulkAccounts {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Select at most %d accounts at a time.", maxBulkAccounts)}
	}
	if in.IsActive == nil && in.IsVisible == nil {
		return nil, ClientError{Msg: "Specify isActive, isVisible, or both."}
	}

	// Sort and dedupe before locking anything: two overlapping batches
	// submitted in opposite client order otherwise acquire loadCurrent's
	// FOR UPDATE row locks in opposite order and deadlock (Postgres aborts
	// one with SQLSTATE 40P01). A single global lock order per transaction
	// removes that regardless of request order. This is not cosmetic --
	// leave it in.
	seen := make(map[string]bool, len(in.UUIDs))
	uuids := make([]string, 0, len(in.UUIDs))
	for _, u := range in.UUIDs {
		if seen[u] {
			continue
		}
		seen[u] = true
		uuids = append(uuids, u)
	}
	sort.Strings(uuids)

	for _, uuid := range uuids {
		if !validAccountUUID(uuid) {
			return nil, ClientError{Msg: fmt.Sprintf("%q is not a valid account id.", uuid)}
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin bulk update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	results := make([]BulkResult, 0, len(uuids))
	for _, uuid := range uuids {
		res, err := bulkOne(ctx, tx, uuid, in, employeeID)
		if err != nil {
			return nil, err // hard failure: roll everything back
		}
		results = append(results, res)
		if !res.OK {
			// A blocked account fails the whole batch, so the caller never sees
			// a partially applied change.
			return nil, ConflictError{Msg: fmt.Sprintf(
				"No accounts were changed. %s", res.Message)}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit bulk update: %w", err)
	}
	return results, nil
}

// bulkOne applies the batch's flags to one account. A ConflictError becomes a
// non-OK result rather than an error, so the caller can report which account
// blocked the batch. It takes pgx.Tx, not the wider rowQuerier, so the
// all-or-nothing guarantee is type-enforced rather than resting on the caller
// happening to pass a transaction (a *pgxpool.Pool also satisfies rowQuerier
// and would autocommit each row).
func bulkOne(ctx context.Context, tx pgx.Tx, uuid string, in BulkInput, employeeID int) (BulkResult, error) {
	cur, err := loadCurrent(ctx, tx, uuid)
	if errors.Is(err, ErrNotFound) {
		return BulkResult{UUID: uuid, OK: false, Message: "Account not found."}, nil
	}
	if err != nil {
		return BulkResult{}, err
	}

	next := *cur
	var audits []historyRow

	if in.IsActive != nil && *in.IsActive != cur.isActive {
		next.isActive = *in.IsActive
		act := actionActivate
		if !next.isActive {
			act = actionDeactivate
		}
		audits = append(audits, historyRow{AccountID: &cur.id, Action: act,
			Field: "is_active", OldValue: boolStr(cur.isActive),
			NewValue: boolStr(next.isActive), EmployeeID: employeeID})
	}
	// AD-8: active implies visible. When the caller activates a hidden
	// account without also specifying isVisible, that is an implicit un-hide,
	// not a request to hide it -- without this, the check further down would
	// reject the activation with a message describing the opposite
	// transition ("must be deactivated before it can be hidden").
	if in.IsActive != nil && *in.IsActive && in.IsVisible == nil && !cur.isVisible {
		next.isVisible = true
		audits = append(audits, historyRow{AccountID: &cur.id, Action: actionShow,
			Field: "is_visible", OldValue: boolStr(cur.isVisible),
			NewValue: boolStr(next.isVisible), EmployeeID: employeeID})
	}
	if in.IsVisible != nil && *in.IsVisible != cur.isVisible {
		next.isVisible = *in.IsVisible
		act := actionShow
		if !next.isVisible {
			act = actionHide
		}
		audits = append(audits, historyRow{AccountID: &cur.id, Action: act,
			Field: "is_visible", OldValue: boolStr(cur.isVisible),
			NewValue: boolStr(next.isVisible), EmployeeID: employeeID})
	}

	if len(audits) == 0 {
		return BulkResult{UUID: uuid, OK: true, Message: "No change."}, nil
	}

	retiring := (!next.isActive && cur.isActive) || (!next.isVisible && cur.isVisible)
	if retiring {
		if err := guardRetire(ctx, tx, cur.id, cur.code, cur.name); err != nil {
			if conflict, ok := IsConflict(err); ok {
				return BulkResult{UUID: uuid, OK: false, Message: conflict.Msg}, nil
			}
			return BulkResult{}, err
		}
	}
	if next.isActive && !next.isVisible {
		return BulkResult{UUID: uuid, OK: false,
			Message: fmt.Sprintf("Account %s must be deactivated before it can be hidden.", cur.code)}, nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE coa_account
		SET coa_account_is_active = $2, coa_account_is_visible = $3,
		    coa_account_updated_at = CURRENT_TIMESTAMP, coa_account_updated_by = $4,
		    coa_account_record_version = coa_account_record_version + 1
		WHERE coa_account_id = $1 AND coa_account_deleted_at IS NULL`,
		cur.id, next.isActive, next.isVisible, nullableInt(employeeID)); err != nil {
		return BulkResult{}, fmt.Errorf("bulk update account %s: %w", cur.code, err)
	}
	for _, a := range audits {
		if err := appendHistory(ctx, tx, a); err != nil {
			return BulkResult{}, err
		}
	}
	return BulkResult{UUID: uuid, OK: true}, nil
}
