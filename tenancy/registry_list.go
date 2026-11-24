package tenancy

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrInvalidTenantCursor is returned by ListTenantsPage for a cursor the server
// did not issue (tampered, truncated, or from another list).
var ErrInvalidTenantCursor = errors.New("invalid tenant list cursor")

const (
	// MaxTenantPageSize caps one ListTenantsPage request; ListTenants walks the
	// whole table in pages of this size.
	MaxTenantPageSize = 200

	tenantCursorSep = "|"
)

// encodeTenantCursor builds the opaque keyset cursor for the last row of a page.
// The list is ordered (created_at DESC, id DESC), so the pair is unique and
// strictly decreasing.
func encodeTenantCursor(createdAt time.Time, id string) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + tenantCursorSep + id
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeTenantCursor(cursor string) (time.Time, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", ErrInvalidTenantCursor
	}
	ts, id, ok := strings.Cut(string(raw), tenantCursorSep)
	if !ok || id == "" {
		return time.Time{}, "", ErrInvalidTenantCursor
	}
	createdAt, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, "", ErrInvalidTenantCursor
	}
	return createdAt, id, nil
}

// ListTenantsPage returns up to limit tenants, newest first (platform admin
// view), plus the cursor for the next page ("" when this was the last). Pass
// the previous page's cursor to continue; "" starts from the newest tenant.
// limit is clamped to [1, MaxTenantPageSize].
func (c *ControlPlane) ListTenantsPage(ctx context.Context, limit int, cursor string) ([]Tenant, string, error) {
	if limit < 1 {
		limit = 1
	}
	if limit > MaxTenantPageSize {
		limit = MaxTenantPageSize
	}

	q := "SELECT " + tenantColumns + " FROM tenants "
	args := []any{}
	if cursor != "" {
		createdAt, id, err := decodeTenantCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		q += "WHERE (created_at, id) < ($1::timestamptz, $2::uuid) "
		args = append(args, createdAt, id)
	}
	// One extra row tells us whether a next page exists without a COUNT.
	q += fmt.Sprintf("ORDER BY created_at DESC, id DESC LIMIT $%d", len(args)+1)
	args = append(args, limit+1)

	rows, err := c.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, "", fmt.Errorf("list tenants page: %w", err)
	}
	defer rows.Close()

	out := make([]Tenant, 0, limit)
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, "", err
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list tenants page: %w", err)
	}

	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		next = encodeTenantCursor(last.CreatedAt, last.ID)
	}
	return out, next, nil
}

// ListTenants returns every tenant, newest first. Background jobs (migrations,
// automation, sweeps, backfills) rely on it seeing all tenants, so it walks the
// table in keyset batches rather than imposing a row cap; the platform-admin
// HTTP list uses ListTenantsPage instead.
func (c *ControlPlane) ListTenants(ctx context.Context) ([]Tenant, error) {
	var all []Tenant
	cursor := ""
	for {
		page, next, err := c.ListTenantsPage(ctx, MaxTenantPageSize, cursor)
		if err != nil {
			return nil, fmt.Errorf("list tenants: %w", err)
		}
		all = append(all, page...)
		if next == "" {
			return all, nil
		}
		cursor = next
	}
}
