package crmstore

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// ChildHit is one crm_activity or customer_note match. It carries the PARENT
// customer's uuid and name because these child records have no detail page of
// their own -- global search routes the hit to the customer detail page.
type ChildHit struct {
	ID           string // the activity / note uuid
	CustomerUUID string // parent customer uuid -- the routable id
	CustomerName string // parent customer name -- shown as the subtitle
	Snippet      string // subject or body text to display
	Kind         string // crm_activity.activity_type; "" for customer_note
	UpdatedAt    time.Time
}

// SearchActivity matches crm_activity subject/body for term, newest-updated
// first, limited to limit+1 rows (the extra row signals hasMore). When scope is
// not "all" the results are narrowed to customers the caller owns; an identity
// with no employee row sees nothing (fail-closed).
func SearchActivity(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID, term string, limit int) ([]ChildHit, bool, error) {
	return searchCustomerChild(ctx, pool, scope, actorIdentityID, term, limit, childQuery{
		table:   "crm_activity a",
		idExpr:  "a.crm_activity_uuid::text",
		match:   "(a.subject ILIKE '%'||$1||'%' OR a.body ILIKE '%'||$1||'%')",
		snippet: "CASE WHEN a.subject <> '' THEN a.subject ELSE a.body END",
		kind:    "a.activity_type",
		live:    "a.deleted_at IS NULL",
	})
}

// SearchCustomerNotes matches customer_note body for term, same scoping/ordering
// as SearchActivity.
func SearchCustomerNotes(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID, term string, limit int) ([]ChildHit, bool, error) {
	return searchCustomerChild(ctx, pool, scope, actorIdentityID, term, limit, childQuery{
		table:   "customer_note a",
		idExpr:  "a.customer_note_uuid::text",
		match:   "a.body ILIKE '%'||$1||'%'",
		snippet: "a.body",
		kind:    "''",
		live:    "a.deleted_at IS NULL",
	})
}

// childQuery is the per-table shape searchCustomerChild plugs into one query.
// Every field is fixed package code -- never client input.
type childQuery struct {
	table, idExpr, match, snippet, kind, live string
}

func searchCustomerChild(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID, term string, limit int, cq childQuery) ([]ChildHit, bool, error) {
	term = query.StripLikeMeta(strings.TrimSpace(term))
	if term == "" || limit <= 0 {
		return nil, false, nil
	}

	args := []any{term}
	scopeSQL := ""
	if scope != string(authz.ScopeAll) {
		empID, ok := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !ok {
			return nil, false, nil // no employee row => nothing in scope
		}
		args = append(args, empID)
		scopeSQL = " AND c.customer_crm_owner_user_id = $" + strconv.Itoa(len(args))
	}
	args = append(args, limit+1)
	limPos := strconv.Itoa(len(args))

	sql := `
		SELECT ` + cq.idExpr + `, c.customer_uuid::text, c.customer_name,
		       ` + cq.snippet + `, ` + cq.kind + `, a.updated_at
		  FROM ` + cq.table + `
		  JOIN customer c ON c.customer_id = a.customer_id
		 WHERE ` + cq.live + ` AND c.customer_deleted_at IS NULL
		   AND ` + cq.match + scopeSQL + `
		 ORDER BY a.updated_at DESC
		 LIMIT $` + limPos

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, false, fmt.Errorf("search customer child: %w", err)
	}
	defer rows.Close()

	var out []ChildHit
	for rows.Next() {
		var h ChildHit
		if err := rows.Scan(&h.ID, &h.CustomerUUID, &h.CustomerName, &h.Snippet, &h.Kind, &h.UpdatedAt); err != nil {
			return nil, false, fmt.Errorf("scan customer child: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("search customer child: %w", err)
	}

	hasMore := false
	if len(out) > limit {
		out, hasMore = out[:limit], true
	}
	return out, hasMore, nil
}
