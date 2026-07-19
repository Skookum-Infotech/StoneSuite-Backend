// Package auditstore is the tenant-scoped read model for the audit trail
// (audit_logs). It is a browse/list surface — distinct from the workflow/CRM
// record filter engine in query/ — with parameterized filters, RBAC scope
// narrowing on the actor, and opaque keyset pagination. It is append-only from
// the reader's side: no writes live here.
package auditstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MaxLimit / DefaultLimit bound the page size, mirroring the record query engine.
const (
	MaxLimit     = 100
	DefaultLimit = 25
)

// Scope values narrow which actors' entries are visible. Precedence all>own.
const (
	ScopeAll = "all"
	ScopeOwn = "own"
)

// ErrInvalidCursor is returned when a pagination cursor cannot be decoded.
var ErrInvalidCursor = errors.New("invalid pagination cursor")

// Entry is one audit-log row in the browse view.
type Entry struct {
	ID          string          `json:"id"`
	ActorUserID *string         `json:"actor_user_id"`
	Action      string          `json:"action"`
	Resource    string          `json:"resource"`
	ResourceID  string          `json:"resource_id"`
	Details     json.RawMessage `json:"details"`
	CreatedAt   time.Time       `json:"created_at"`
}

// Filter is the parameterized query surface for the audit browser.
type Filter struct {
	Resource string     // optional exact match
	Action   string     // optional exact match
	Actor    string     // optional actor_user_id exact match
	From     *time.Time // optional created_at >=
	To       *time.Time // optional created_at <=
	Limit    int        // clamped to [1, MaxLimit]; 0 -> DefaultLimit
	Cursor   string     // opaque keyset cursor from a previous page

	// Scope narrows visible actors; CallerUserID is the tenant users.id of the
	// requester (required for own scope).
	Scope        string
	CallerUserID string
}

// List returns a page of audit entries newest-first and an opaque cursor for the
// next page ("" when the page is the last one).
func List(ctx context.Context, pool *pgxpool.Pool, f Filter) ([]Entry, string, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	var conds []string
	var args []any
	param := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	if f.Resource != "" {
		conds = append(conds, "resource = "+param(f.Resource))
	}
	if f.Action != "" {
		conds = append(conds, "action = "+param(f.Action))
	}
	if f.Actor != "" {
		conds = append(conds, "actor_user_id = "+param(f.Actor))
	}
	if f.From != nil {
		conds = append(conds, "created_at >= "+param(*f.From))
	}
	if f.To != nil {
		conds = append(conds, "created_at <= "+param(*f.To))
	}

	// Scope narrows the actor set. ScopeAll adds nothing.
	switch f.Scope {
	case ScopeOwn:
		conds = append(conds, "actor_user_id = "+param(f.CallerUserID))
	}

	// Keyset: created_at DESC, id DESC. "after" the cursor means strictly less.
	if f.Cursor != "" {
		ts, id, err := decodeCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		tsP, idP := param(ts), param(id)
		conds = append(conds, "(created_at < "+tsP+" OR (created_at = "+tsP+" AND id < "+idP+"))")
	}

	where := ""
	if len(conds) > 0 {
		where = "WHERE " + strings.Join(conds, " AND ")
	}
	// Fetch limit+1 to detect whether another page follows.
	sql := `SELECT id, actor_user_id, action, resource, resource_id, details, created_at
		FROM audit_logs ` + where + `
		ORDER BY created_at DESC, id DESC
		LIMIT ` + param(limit+1)

	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	entries := make([]Entry, 0, limit)
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ID, &e.ActorUserID, &e.Action, &e.Resource, &e.ResourceID, &e.Details, &e.CreatedAt); err != nil {
			return nil, "", fmt.Errorf("scan audit log: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list audit logs: %w", err)
	}

	next := ""
	if len(entries) > limit {
		last := entries[limit-1]
		entries = entries[:limit]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	return entries, next, nil
}

// encodeCursor builds an opaque base64 cursor from the last row's sort key.
func encodeCursor(ts time.Time, id string) string {
	raw := ts.UTC().Format(time.RFC3339Nano) + "|" + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor reverses encodeCursor.
func decodeCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", ErrInvalidCursor
	}
	ts, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", ErrInvalidCursor
	}
	return ts, parts[1], nil
}
