//go:build dbtest

package tenancy

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func countRows(t *testing.T, cp *ControlPlane, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, cp.pool.QueryRow(context.Background(), query, args...).Scan(&n))
	return n
}

func TestPurgeTenant_RemovesTenantAndCascadesChildren_DB(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	_, err := cp.CreateInvite(ctx, tenantID, "owner-"+suffix+"@example.com", "tok-"+suffix, time.Now().Add(time.Hour))
	require.NoError(t, err)
	_, err = cp.CreateSSOIdentity(ctx, tenantID, "user-"+suffix+"@example.com", "User", "entra", "subject-"+suffix)
	require.NoError(t, err)
	require.NoError(t, cp.LogPlatformAudit(ctx, "", "admin@example.com", tenantID, "tenant.created", "{}"))

	require.NoError(t, cp.PurgeTenant(ctx, tenantID))

	_, err = cp.TenantByID(ctx, tenantID)
	assert.ErrorIs(t, err, ErrTenantNotFound)
	assert.Zero(t, countRows(t, cp, `SELECT COUNT(*) FROM tenant_invites WHERE tenant_id = $1`, tenantID), "invites cascade")
	assert.Zero(t, countRows(t, cp, `SELECT COUNT(*) FROM identities WHERE tenant_id = $1`, tenantID), "identities cascade")
}

// The audit trail is the only record that a purged tenant ever existed, so it
// must survive the delete (tenant_id is nulled, the row stays).
func TestPurgeTenant_KeepsAuditRowsWithNulledTenant_DB(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	marker := fmt.Sprintf("tenant.purge-test-%d", time.Now().UnixNano())
	require.NoError(t, cp.LogPlatformAudit(ctx, "", "admin@example.com", tenantID, marker, `{"slug":"kept"}`))

	require.NoError(t, cp.PurgeTenant(ctx, tenantID))

	assert.Equal(t, 1, countRows(t, cp,
		`SELECT COUNT(*) FROM platform_audit_logs WHERE action = $1 AND tenant_id IS NULL AND details->>'slug' = 'kept'`, marker))
}

func TestPurgeTenant_UnknownTenant_DB(t *testing.T) {
	cp := newCPTestControlPlane(t)

	err := cp.PurgeTenant(context.Background(), "00000000-0000-0000-0000-000000000000")

	assert.ErrorIs(t, err, ErrTenantNotFound)
}

// Defence in depth: the handler refuses the platform owner first, but the store
// must independently refuse too so no future caller can delete the workspace
// the platform admin signs in to.
func TestPurgeTenant_RefusesPlatformOwner_DB(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	owner, err := cp.CreateTenant(ctx, "owner-purge-test-"+suffix, "Owner Purge Test", true)
	require.NoError(t, err)
	// is_platform_owner is a database-enforced singleton: delete this row when
	// the test ends so it never blocks the next owner-creating test.
	t.Cleanup(func() {
		_, _ = cp.pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, owner.ID)
	})

	err = cp.PurgeTenant(ctx, owner.ID)

	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrPlatformOwnerProtected), "got %v", err)
	_, err = cp.TenantByID(ctx, owner.ID)
	assert.NoError(t, err, "the owner tenant must still exist")
}
