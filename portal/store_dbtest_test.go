//go:build dbtest

package portal

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testTenantPool connects to the tenant-schema test database (the same
// TEST_DATABASE_URL the controllers dbtest suite uses). Skips cleanly when unset.
func testTenantPool(t *testing.T) *pgxpool.Pool {
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

// seedCustomer inserts a minimal live CUST-stage customer and returns its
// internal id and uuid.
func seedCustomer(t *testing.T, pool *pgxpool.Pool) (int, string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID))

	var (
		id   int
		uuid string
	)
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_id, customer_uuid`,
		custTypeID, "Portal Guard Test "+suffix).Scan(&id, &uuid))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM customer WHERE customer_id = $1`, id)
	})
	return id, uuid
}

// seedPortalUser attaches a portal login in the given status to a customer.
func seedPortalUser(t *testing.T, pool *pgxpool.Pool, customerID int, status string) {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	email := "portal-" + suffix + "@example.com"
	var id string
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO customer_portal_user (identity_id, customer_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, $2, 'Portal User', $3) RETURNING id`,
		customerID, email, status).Scan(&id))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM customer_portal_user WHERE id = $1`, id)
	})
}

func TestHasLivePortalUsers(t *testing.T) {
	pool := testTenantPool(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		statuses []string
		want     bool
	}{
		{"no portal users", nil, false},
		{"one active login", []string{StatusActive}, true},
		{"one suspended login", []string{StatusSuspended}, true},
		{"only revoked logins", []string{StatusRevoked}, false},
		{"revoked plus active", []string{StatusRevoked, StatusActive}, true},
		{"revoked plus suspended", []string{StatusRevoked, StatusSuspended}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, uuid := seedCustomer(t, pool)
			for _, s := range tt.statuses {
				seedPortalUser(t, pool, id, s)
			}

			got, err := HasLivePortalUsers(ctx, pool, uuid)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestHasLivePortalUsers_ScopedToCustomer confirms a live login on one customer
// does not make an unrelated customer look like it still has portal access.
func TestHasLivePortalUsers_ScopedToCustomer(t *testing.T) {
	pool := testTenantPool(t)
	ctx := context.Background()

	withAccessID, _ := seedCustomer(t, pool)
	seedPortalUser(t, pool, withAccessID, StatusActive)

	_, cleanUUID := seedCustomer(t, pool)

	got, err := HasLivePortalUsers(ctx, pool, cleanUUID)
	require.NoError(t, err)
	assert.False(t, got, "customer with no portal logins of its own must report false")
}

// TestListUsersForTenant_ExcludesDeletedCustomers confirms the tenant-wide
// roster drops logins whose owning customer has been soft-deleted — otherwise
// each such row shows a "Manage" link into a customer record that 404s.
func TestListUsersForTenant_ExcludesDeletedCustomers(t *testing.T) {
	pool := testTenantPool(t)
	ctx := context.Background()

	liveID, liveUUID := seedCustomer(t, pool)
	seedPortalUser(t, pool, liveID, StatusActive)

	deletedID, deletedUUID := seedCustomer(t, pool)
	seedPortalUser(t, pool, deletedID, StatusRevoked)
	_, err := pool.Exec(ctx,
		`UPDATE customer SET customer_deleted_at = NOW(), customer_deleted_by = 1 WHERE customer_id = $1`, deletedID)
	require.NoError(t, err)

	users, err := ListUsersForTenant(ctx, pool)
	require.NoError(t, err)

	seen := map[string]bool{}
	for _, u := range users {
		seen[u.CustomerUUID] = true
	}
	assert.True(t, seen[liveUUID], "login on a live customer must appear in the roster")
	assert.False(t, seen[deletedUUID], "login on a soft-deleted customer must not appear in the roster")
}
