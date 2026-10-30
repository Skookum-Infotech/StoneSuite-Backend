//go:build dbtest

package authz

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEffectiveGrants_DeactivatedUserLosesWarmCachedGrants_DB pins that a warm
// cache entry never outlives a deactivation made behind this process's back
// (another machine, or SQL): the status is re-checked on every cache hit.
func TestEffectiveGrants_DeactivatedUserLosesWarmCachedGrants_DB(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var identityID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&identityID))
	var userID, roleID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (identity_id, email, status) VALUES ($1, $2, 'active') RETURNING id`,
		identityID, identityID+"@grants-cache.test").Scan(&userID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO roles (key, name) VALUES ($1, 'grants cache test') RETURNING id`,
		"gc_"+identityID).Scan(&roleID))
	_, err = pool.Exec(ctx, `INSERT INTO role_permissions (role_id, resource, action, scope) VALUES ($1, 'quote', 'read', 'all')`, roleID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, userID, roleID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE id = $1`, roleID)
		invalidateGrants(pool)
	})

	warm, err := EffectiveGrants(ctx, pool, identityID)
	require.NoError(t, err)
	require.Len(t, warm, 1, "active user resolves grants (and warms the cache)")
	again, err := EffectiveGrants(ctx, pool, identityID)
	require.NoError(t, err)
	require.Len(t, again, 1, "active user still served on a cache hit")

	// Deactivate directly in the DB: no InvalidateGrants, as another machine would.
	_, err = pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, userID)
	require.NoError(t, err)

	got, err := EffectiveGrants(ctx, pool, identityID)
	require.NoError(t, err)
	assert.Empty(t, got, "deactivated user must get no grants despite a warm cache")

	// Reactivating must not resurrect the evicted entry from stale data.
	_, err = pool.Exec(ctx, `UPDATE users SET status = 'active' WHERE id = $1`, userID)
	require.NoError(t, err)
	got, err = EffectiveGrants(ctx, pool, identityID)
	require.NoError(t, err)
	assert.Len(t, got, 1)
}
