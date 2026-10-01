//go:build dbtest

package auditstore

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTenantTestPool connects to the tenant-schema test database (audit_logs
// lives there). Skips cleanly when TEST_DATABASE_URL is unset.
func newTenantTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// TestList_FoldsOldValueIntoDetails pins the fix for the tenant-wide audit log
// showing "Record created." on every update: the browse view must return the
// before-snapshot (stored in the old_value column) as details.old so the client
// can diff it against details.new. A create has no old_value and must not
// gain an "old" key.
func TestList_FoldsOldValueIntoDetails(t *testing.T) {
	pool := newTenantTestPool(t)
	ctx := context.Background()

	// Unique resource so the assertions never see other rows.
	resource := fmt.Sprintf("auditstore_test_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM audit_logs WHERE resource = $1`, resource)
	})

	_, err := pool.Exec(ctx, `
		INSERT INTO audit_logs (action, resource, resource_id, details, old_value, new_value)
		VALUES ('create', $1, 'rec-1', '{"new":{"status":"draft"}}'::jsonb, NULL, '{"status":"draft"}'::jsonb)`, resource)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO audit_logs (action, resource, resource_id, details, old_value, new_value)
		VALUES ('update', $1, 'rec-1', '{"new":{"status":"sent"},"reason":"x"}'::jsonb,
		        '{"status":"draft"}'::jsonb, '{"status":"sent"}'::jsonb)`, resource)
	require.NoError(t, err)

	entries, _, err := List(ctx, pool, Filter{Resource: resource, Scope: ScopeAll})
	require.NoError(t, err)
	require.Len(t, entries, 2)

	byAction := map[string]map[string]json.RawMessage{}
	for _, e := range entries {
		var d map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(e.Details, &d))
		byAction[e.Action] = d
	}

	upd := byAction["update"]
	assert.JSONEq(t, `{"status":"draft"}`, string(upd["old"]), "update must expose the before-snapshot as details.old")
	assert.JSONEq(t, `{"status":"sent"}`, string(upd["new"]), "existing details.new must be preserved")
	assert.JSONEq(t, `"x"`, string(upd["reason"]), "extra metadata must be preserved")

	crt := byAction["create"]
	_, hasOld := crt["old"]
	assert.False(t, hasOld, "a create has no old_value, so details must not gain an old key")
	assert.JSONEq(t, `{"status":"draft"}`, string(crt["new"]))
}
