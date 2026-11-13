package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Bounds of the per-customer takeover limits (mirrored by CHECK constraints
// on tenants). 0 turns a limit off.
const (
	MaxTakeoverCooldownMinutes = 1440
	MaxTakeoverDailyCap        = 100
)

// ErrPlatformOwnerExempt is returned when a rule the platform-owner tenant is
// always exempt from is switched on for it.
var ErrPlatformOwnerExempt = errors.New("platform-owner tenant is exempt")

// SessionPolicyPatch changes a customer's session settings; nil fields are
// left as they are.
type SessionPolicyPatch struct {
	Enforced        *bool
	CooldownMinutes *int
	DailyCap        *int
	// Grace is the handover given to the sessions ended when the rule is
	// switched on (see UpdateTenantSessionPolicy).
	Grace time.Duration
}

// UpdateTenantSessionPolicy applies patch to a customer tenant and returns
// the sessions it ended. Switching the rule on ends, in the same transaction,
// every live session but the most recently used one of each staff user of
// the tenant (platform admins stay exempt), so nobody keeps two browsers
// signed in from before the rule applied; the ended browsers get the usual
// countdown. The platform-owner tenant is always exempt: turning the rule on
// there returns ErrPlatformOwnerExempt and changes nothing. Range checks are
// the caller's; the table's CHECK constraints back them up.
func (c *ControlPlane) UpdateTenantSessionPolicy(ctx context.Context, tenantID string, patch SessionPolicyPatch) ([]string, error) {
	defer c.forgetTenant(tenantID)
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("update session policy: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	var isOwner, wasEnforced bool
	err = tx.QueryRow(ctx,
		`SELECT is_platform_owner, single_session_enforced FROM tenants WHERE id = $1 FOR UPDATE`, tenantID,
	).Scan(&isOwner, &wasEnforced)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update session policy: load tenant: %w", err)
	}
	if isOwner && patch.Enforced != nil && *patch.Enforced {
		return nil, ErrPlatformOwnerExempt
	}
	if _, err := tx.Exec(ctx,
		`UPDATE tenants SET
		   single_session_enforced   = COALESCE($2, single_session_enforced) AND NOT is_platform_owner,
		   takeover_cooldown_minutes = COALESCE($3, takeover_cooldown_minutes),
		   takeover_daily_cap        = COALESCE($4, takeover_daily_cap),
		   updated_at = NOW()
		 WHERE id = $1`, tenantID, patch.Enforced, patch.CooldownMinutes, patch.DailyCap); err != nil {
		return nil, fmt.Errorf("update session policy: %w", err)
	}
	var ended []string
	if !wasEnforced && patch.Enforced != nil && *patch.Enforced {
		if ended, err = endDuplicateSessions(ctx, tx, tenantID, patch.Grace); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("update session policy: commit: %w", err)
	}
	return ended, nil
}

// endDuplicateSessions replaces every live session of the tenant's staff
// identities except each identity's most recently used one.
func endDuplicateSessions(ctx context.Context, q sessionQuerier, tenantID string, grace time.Duration) ([]string, error) {
	rows, err := q.Query(ctx,
		`WITH live AS (
		   SELECT s.id, s.identity_id,
		          FIRST_VALUE(s.id) OVER (PARTITION BY s.identity_id ORDER BY s.last_seen_at DESC, s.created_at DESC) AS keep_id
		   FROM auth_sessions s
		   JOIN identities i ON i.id = s.identity_id
		   WHERE i.tenant_id = $1 AND s.ended_at IS NULL AND s.replaced_at IS NULL
		     AND NOT EXISTS (SELECT 1 FROM platform_admins pa WHERE pa.identity_id = s.identity_id)
		 )
		 UPDATE auth_sessions a
		 SET replaced_at = NOW(), replaced_by = live.keep_id, replaced_reason = $3,
		     ends_at = NOW() + $2::bigint * INTERVAL '1 millisecond'
		 FROM live
		 WHERE a.id = live.id AND live.id <> live.keep_id
		 RETURNING a.id`, tenantID, grace.Milliseconds(), ReplacePolicy)
	if err != nil {
		return nil, fmt.Errorf("end duplicate sessions: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("end duplicate sessions: collect: %w", err)
	}
	return ids, nil
}
