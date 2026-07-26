package chartofaccounts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxBulkAccounts caps one bulk request. The tenant ships with 127 accounts,
// so this comfortably covers "select all" while bounding the transaction.
const maxBulkAccounts = 500

// lockOrderedUUIDs lowercases, dedupes and sorts the requested ids so every
// bulk transaction acquires loadCurrent's FOR UPDATE row locks in one global
// order. Two overlapping batches submitted in opposite client order otherwise
// lock the same rows in opposite order and deadlock, and Postgres aborts one
// with SQLSTATE 40P01 -- surfaced as a 500 for what is really retryable
// contention.
//
// The lowercasing is as load-bearing as the sort. Postgres compares uuid values
// case-insensitively while Go's byte sort does not ('A' is 0x41, 'a' is 0x61),
// so without it a case variant sorts to a different position than the twin row
// it names and the "global" order is not global. It also collapses
// ["bbbb-...-02","BBBB-...-02"] to one entry instead of visiting one row twice.
func lockOrderedUUIDs(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, u := range in {
		u = strings.ToLower(u)
		if seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

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

	uuids := lockOrderedUUIDs(in.UUIDs)
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
			// Any blocked account fails the whole batch, so the caller never
			// sees a partially applied change. Prefix the reason but keep the
			// original error's type and payload -- guardRetire's ConflictError
			// carries BlockingSlots, and the UI needs those to tell the user
			// which default slots to repoint before retrying.
			if conflict, ok := IsConflict(err); ok {
				conflict.Msg = "No accounts were changed. " + conflict.Msg
				return nil, conflict
			}
			return nil, err
		}
		results = append(results, res)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit bulk update: %w", err)
	}
	return results, nil
}

// bulkOne applies the batch's flags to one account, returning whether that
// account actually changed. Anything that blocks the account is returned as an
// error, not as a result: the batch is all-or-nothing, so a "failed" result
// could never reach the caller anyway -- BulkUpdate aborts on the first one.
//
// It takes pgx.Tx, not the wider rowQuerier, so the all-or-nothing guarantee is
// type-enforced rather than resting on the caller happening to pass a
// transaction (a *pgxpool.Pool also satisfies rowQuerier and would autocommit
// each row).
func bulkOne(ctx context.Context, tx pgx.Tx, uuid string, in BulkInput, employeeID int) (BulkResult, error) {
	cur, err := loadCurrent(ctx, tx, uuid)
	if errors.Is(err, ErrNotFound) {
		return BulkResult{}, ConflictError{Msg: fmt.Sprintf("Account %s was not found.", uuid)}
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
		return BulkResult{UUID: uuid, Changed: false}, nil
	}

	retiring := (!next.isActive && cur.isActive) || (!next.isVisible && cur.isVisible)
	if retiring {
		// Returned as-is: guardRetire's ConflictError carries BlockingSlots,
		// and flattening it to a message here is what used to strip that
		// payload out of every bulk 409 response.
		if err := guardRetire(ctx, tx, cur.id, cur.code, cur.name); err != nil {
			return BulkResult{}, err
		}
	}
	if next.isActive && !next.isVisible {
		return BulkResult{}, ConflictError{Msg: fmt.Sprintf(
			"Account %s must be deactivated before it can be hidden.", cur.code)}
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
	return BulkResult{UUID: uuid, Changed: true}, nil
}
