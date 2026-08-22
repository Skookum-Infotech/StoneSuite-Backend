//go:build dbtest

package controllers

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

func testTenantPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestQueryCurrencyLookupItems_IncludesSymbol confirms the currency lookup
// query returns a populated display symbol for each active currency, not
// just id/code/name -- the frontend needs the real symbol (e.g. "$") to
// render amounts instead of concatenating the currency code onto a
// hardcoded prefix.
func TestQueryCurrencyLookupItems_IncludesSymbol(t *testing.T) {
	pool := testTenantPool(t)
	ctx := context.Background()

	items, err := queryCurrencyLookupItems(ctx, pool)
	require.NoError(t, err)
	require.NotEmpty(t, items)

	var usd *CurrencyLookupItem
	for i := range items {
		assert.NotEmpty(t, items[i].Symbol, "currency %q missing symbol", items[i].Code)
		if items[i].Code == "USD" {
			usd = &items[i]
		}
	}
	require.NotNil(t, usd, "seeded USD currency not found")
	assert.Equal(t, "$", usd.Symbol)
}

// seedTestUser inserts a tenant user (with a fresh control-plane-style
// identity_id) and returns its id.
func seedTestUser(t *testing.T, pool *pgxpool.Pool, fullName string) string {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var userID string
	err := pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, $2, 'active') RETURNING id`,
		fullName+"-"+suffix+"@test.local", fullName).Scan(&userID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
	return userID
}

// seedTestEmployee inserts an employee linked to userID and returns its id.
func seedTestEmployee(t *testing.T, pool *pgxpool.Pool, userID string, active bool) int {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var employeeID int
	err := pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_is_active, employee_created_by)
		VALUES ($1, 'Test', 'Employee', $2, $3, 1) RETURNING employee_id`,
		userID, "emp-"+suffix+"@test.local", active).Scan(&employeeID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM employee WHERE employee_id = $1`, employeeID)
	})
	return employeeID
}

// seedTestRoleWithPermission creates a role granting {resource, action, scope:
// all} and assigns it to userID.
func seedTestRoleWithPermission(t *testing.T, pool *pgxpool.Pool, userID, resource, action string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var roleID string
	err := pool.QueryRow(ctx, `
		INSERT INTO roles (key, name) VALUES ($1, $1) RETURNING id`,
		"test_role_"+suffix).Scan(&roleID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM roles WHERE id = $1`, roleID)
	})

	_, err = pool.Exec(ctx, `
		INSERT INTO role_permissions (role_id, resource, action, scope) VALUES ($1, $2, $3, 'all')`,
		roleID, resource, action)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)`, userID, roleID)
	require.NoError(t, err)
}

// TestQueryEligibleSalesRepEmployees_FiltersToEligibleStaff confirms the
// Sales Rep employee picker only returns staff who actually hold create or
// update permission on a CRM resource, excluding employees with no such
// permission and excluding deactivated employees even if their role would
// otherwise qualify.
func TestQueryEligibleSalesRepEmployees_FiltersToEligibleStaff(t *testing.T) {
	pool := testTenantPool(t)
	ctx := context.Background()

	eligibleUser := seedTestUser(t, pool, "Eligible Rep")
	seedTestRoleWithPermission(t, pool, eligibleUser, "customer", "create")
	eligibleEmpID := seedTestEmployee(t, pool, eligibleUser, true)

	unrelatedUser := seedTestUser(t, pool, "Unrelated Staff")
	seedTestRoleWithPermission(t, pool, unrelatedUser, "customer", "read")
	unrelatedEmpID := seedTestEmployee(t, pool, unrelatedUser, true)

	deactivatedUser := seedTestUser(t, pool, "Deactivated Rep")
	seedTestRoleWithPermission(t, pool, deactivatedUser, "lead", "update")
	deactivatedEmpID := seedTestEmployee(t, pool, deactivatedUser, false)

	items, err := queryEligibleSalesRepEmployees(ctx, pool)
	require.NoError(t, err)

	ids := map[int]bool{}
	for _, item := range items {
		ids[item.ID] = true
	}
	assert.True(t, ids[eligibleEmpID], "expected create-permitted employee to be eligible")
	assert.False(t, ids[unrelatedEmpID], "read-only employee should not be eligible")
	assert.False(t, ids[deactivatedEmpID], "deactivated employee should not be eligible even with a qualifying role")
}
