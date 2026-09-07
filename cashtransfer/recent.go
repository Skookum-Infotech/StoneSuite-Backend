package cashtransfer

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// ledgerStatusCodes are the statuses that have actually moved money through
// the ledger: POST and RVSD (a reversed entry was posted first). Draft,
// pending-approval, approved and cancelled entries never hit the books, so
// the Accounting snapshot widget must not count or list them.
var ledgerStatusCodes = []string{postedStatusCode, "RVSD"}

// RecentEntry is one row of the Accounting snapshot widget's recent-entries
// list. Description is composed here rather than in the handler so the two
// account names never have to travel separately just to be joined again.
type RecentEntry struct {
	UUID        string
	Number      string
	Description string
	Amount      float64
	Date        time.Time
}

// ledgerWhere builds the shared WHERE clauses for both snapshot queries:
// live, posted entries narrowed to the caller's RBAC scope. ok=false when
// scope is not "all" and the caller has no employee record -- callers return
// an empty result rather than an error, matching Search's convention.
func ledgerWhere(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string) (where []string, args []any, ok bool) {
	where = []string{
		"ct.cash_transfer_deleted_at IS NULL",
		"rs.record_status_code = ANY($1)",
	}
	args = []any{ledgerStatusCodes}
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return nil, nil, false
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("ct.cash_transfer_owner_id = $%d", len(args)))
	}
	return where, args, true
}

// RecentPosted returns the `limit` most recent posted entries, newest first,
// scoped to the caller's RBAC scope. An entry's description is its own
// reference when it has one, and otherwise the accounts it moved money
// between -- so a row is never blank in the widget.
func RecentPosted(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, limit int) ([]RecentEntry, error) {
	where, args, ok := ledgerWhere(ctx, pool, scope, actorIdentityID)
	if !ok {
		return []RecentEntry{}, nil
	}

	q := `
		SELECT ct.cash_transfer_uuid, COALESCE(ct.cash_transfer_number,''),
		       CASE
		           WHEN COALESCE(ct.cash_transfer_reference,'') <> '' THEN ct.cash_transfer_reference
		           ELSE fa.coa_account_name || ' → ' || ta.coa_account_name
		       END,
		       ct.cash_transfer_amount, ct.cash_transfer_date
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		JOIN coa_account fa ON fa.coa_account_id = ct.from_account_id
		JOIN coa_account ta ON ta.coa_account_id = ct.to_account_id
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY ct.cash_transfer_date DESC, ct.cash_transfer_id DESC
		LIMIT ` + strconv.Itoa(limit)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query recent posted entries: %w", err)
	}
	defer rows.Close()

	out := []RecentEntry{}
	for rows.Next() {
		var e RecentEntry
		if err := rows.Scan(&e.UUID, &e.Number, &e.Description, &e.Amount, &e.Date); err != nil {
			return nil, fmt.Errorf("scan recent posted entry: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query recent posted entries: %w", err)
	}
	return out, nil
}

// CountBetween counts posted entries dated within [from, to] -- an accounting
// period's inclusive start/end -- scoped to the caller's RBAC scope. This is
// the widget's "N entries this period" figure, which is deliberately a real
// count across the whole period rather than the length of the truncated list
// RecentPosted returns.
func CountBetween(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, from, to time.Time) (int, error) {
	where, args, ok := ledgerWhere(ctx, pool, scope, actorIdentityID)
	if !ok {
		return 0, nil
	}
	args = append(args, from)
	where = append(where, fmt.Sprintf("ct.cash_transfer_date >= $%d", len(args)))
	args = append(args, to)
	where = append(where, fmt.Sprintf("ct.cash_transfer_date <= $%d", len(args)))

	var count int
	err := pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE `+strings.Join(where, " AND "), args...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count posted entries in period: %w", err)
	}
	return count, nil
}
