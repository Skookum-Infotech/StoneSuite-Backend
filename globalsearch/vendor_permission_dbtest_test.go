//go:build dbtest

package globalsearch

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendors"
)

// vendorSearchPool opens the tenant test database or skips the test.
func vendorSearchPool(t *testing.T) *pgxpool.Pool {
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

// seedSearchIdentity creates a tenant user holding one role with the given
// grants and returns its identity id.
func seedSearchIdentity(t *testing.T, pool *pgxpool.Pool, label string, grants ...authz.Grant) string {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%s-%d", label, time.Now().UnixNano())
	var identityID, userID string
	require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&identityID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (identity_id, email, full_name) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		identityID, suffix+"@gsearch.test", "GSearch "+label).Scan(&userID))
	roleID, err := authz.CreateRole(ctx, pool, "gsearch-"+suffix, "gsearch-"+suffix, "", grants)
	require.NoError(t, err)
	require.NoError(t, authz.AssignRole(ctx, pool, userID, roleID))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1::uuid`, userID)
		_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE id = $1::uuid`, roleID)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, userID)
	})
	return identityID
}

// TestSearch_VendorGroupRequiresVendorRead proves the vendor group is returned
// only to a caller holding vendor:read, even when they can read other modules.
func TestSearch_VendorGroupRequiresVendorRead(t *testing.T) {
	pool := vendorSearchPool(t)
	ctx := context.Background()
	term := fmt.Sprintf("Zyxwv%d", time.Now().UnixNano())

	in := vendors.CreateVendorInput{VendorType: "Organization"}
	in.LegalName = "Vendor " + term
	_, err := vendors.Create(ctx, pool, in, 1)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM vendor WHERE vendor_legal_name = $1`, in.LegalName)
	})

	grant := func(res authz.Resource) authz.Grant {
		return authz.Grant{Resource: res, Action: authz.ActionRead, Scope: authz.ScopeAll}
	}
	tests := []struct {
		name       string
		grants     []authz.Grant
		wantVendor bool
	}{
		{"vendor:read returns vendor hits", []authz.Grant{grant(authz.ResourceVendor)}, true},
		{"other reads without vendor:read return no vendor group", []authz.Grant{grant(authz.ResourceVendorBill), grant(authz.ResourceQuote)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id := seedSearchIdentity(t, pool, "vendor", tc.grants...)
			resp := Search(ctx, pool, id, term, []string{"vendor"}, 0)
			g, has := resp.Groups["vendor"]
			assert.Equal(t, tc.wantVendor, has)
			if tc.wantVendor {
				require.NotEmpty(t, g.Results)
				assert.Equal(t, "vendor", g.Results[0].Type)
			} else {
				assert.Empty(t, g.Results)
			}
		})
	}
}
