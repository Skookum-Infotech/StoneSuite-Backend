//go:build dbtest

package userstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
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

// TestEnsureEmployeeForUser_CreatesAndIsIdempotent covers the actual bug this
// helper exists to close: a users row with no linked employee row is
// invisible to every employee_id-based FK (Sales Rep, approvers, "own"-scope
// ownership). Calling it twice (mirrors login/invite-accept being hit
// repeatedly for the same user) must not create a second row or error.
func TestEnsureEmployeeForUser_CreatesAndIsIdempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	email := "ensure-employee-" + suffix + "@example.test"

	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, 'Ensure Employee', 'active') RETURNING id`,
		email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if err := EnsureEmployeeForUser(ctx, pool, userID, "Ensure Employee", email); err != nil {
		t.Fatalf("EnsureEmployeeForUser (first call): %v", err)
	}
	if err := EnsureEmployeeForUser(ctx, pool, userID, "Ensure Employee", email); err != nil {
		t.Fatalf("EnsureEmployeeForUser (second call): %v", err)
	}

	var count int
	var firstName, lastName string
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM employee WHERE employee_user_id = $1`, userID).Scan(&count); err != nil {
		t.Fatalf("count employee rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("employee rows for user = %d, want exactly 1 (idempotency broken)", count)
	}
	if err := pool.QueryRow(ctx,
		`SELECT employee_first_name, employee_last_name FROM employee WHERE employee_user_id = $1`, userID).
		Scan(&firstName, &lastName); err != nil {
		t.Fatalf("read employee row: %v", err)
	}
	if firstName != "Ensure" || lastName != "Employee" {
		t.Errorf("employee name = (%q, %q), want (\"Ensure\", \"Employee\")", firstName, lastName)
	}
}
