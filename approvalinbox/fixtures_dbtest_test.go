//go:build dbtest

package approvalinbox

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
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

var seq int

func uniq() string {
	seq++
	return fmt.Sprintf("%d%03d", time.Now().UnixNano()%1_000_000_000, seq)
}

type person struct {
	employeeID int
	name       string
}

func seedPerson(t *testing.T, pool *pgxpool.Pool, name string) person {
	t.Helper()
	ctx := context.Background()
	email := "inbox-" + uniq() + "@example.test"
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, $2, 'active') RETURNING id`, email, name).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var emp int
	if err := pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1, 'Inbox', 'Tester', $2, 1) RETURNING employee_id`, userID, email).Scan(&emp); err != nil {
		t.Fatalf("seed employee: %v", err)
	}
	return person{employeeID: emp, name: name}
}

func seedCustomer(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	var typeID, id int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&typeID); err != nil {
		t.Fatalf("CUST type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_id`, typeID, "Inbox Customer "+uniq()).Scan(&id); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return id
}

func typeAndStatus(t *testing.T, pool *pgxpool.Pool, typeCode, statusCode string) (typeID, statusID int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT rt.record_type_id, rs.record_status_id
		FROM lkp_record_type rt JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id
		WHERE rt.record_type_code = $1 AND rs.record_status_code = $2`, typeCode, statusCode).Scan(&typeID, &statusID); err != nil {
		t.Fatalf("resolve %s/%s: %v", typeCode, statusCode, err)
	}
	return typeID, statusID
}

// setApprovers replaces the active approver set of a module's gate (and restores nothing:
// the database is created fresh per package).
func setApprovers(t *testing.T, pool *pgxpool.Pool, table, typeCode, statusCode string, emps ...person) {
	t.Helper()
	typeID, statusID := typeAndStatus(t, pool, typeCode, statusCode)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE record_type_id = $1 AND record_status_id = $2`, table), typeID, statusID); err != nil {
		t.Fatalf("clear approvers: %v", err)
	}
	for _, e := range emps {
		if _, err := pool.Exec(ctx, fmt.Sprintf(
			`INSERT INTO %s (record_type_id, record_status_id, approver_employee_id) VALUES ($1, $2, $3)`, table),
			typeID, statusID, e.employeeID); err != nil {
			t.Fatalf("add approver: %v", err)
		}
	}
}

// seedMemo inserts a credit memo (gate: DRFT, reject in place) owned by owner. createdAt lets the
// ordering tests control entered_at (no history rows -> falls back to created_at).
func seedMemo(t *testing.T, pool *pgxpool.Pool, cust int, owner person, createdAt time.Time) (id int, uuid string) {
	t.Helper()
	typeID, statusID := typeAndStatus(t, pool, "CRDT", "DRFT")
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO credit_memo (record_type, credit_memo_status, credit_memo_customer_id, credit_memo_number,
		                         credit_memo_owner_id, credit_memo_created_by, credit_memo_created_at, credit_memo_approval_status)
		VALUES ($1, $2, $3, $4, $5, 1, $6, 'pending') RETURNING credit_memo_id, credit_memo_uuid::text`,
		typeID, statusID, cust, "CRDT-"+uniq(), owner.employeeID, createdAt).Scan(&id, &uuid); err != nil {
		t.Fatalf("seed credit memo: %v", err)
	}
	return id, uuid
}

func signMemo(t *testing.T, pool *pgxpool.Pool, memoID int, by person) {
	t.Helper()
	_, statusID := typeAndStatus(t, pool, "CRDT", "DRFT")
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO credit_memo_approval (credit_memo_id, record_status_id, approver_employee_id) VALUES ($1, $2, $3)`,
		memoID, statusID, by.employeeID); err != nil {
		t.Fatalf("sign: %v", err)
	}
}

func allowAllChecker(authz.Resource, authz.Action) authz.Decision {
	return authz.Decision{Allowed: true, Scope: authz.ScopeAll}
}

// onlyCreditMemo grants read+transition on credit_memo at the given scope and nothing else, so
// every assertion is about exactly this module regardless of leftovers from other tests.
func onlyCreditMemo(scope authz.Scope) CheckFunc {
	return func(r authz.Resource, a authz.Action) authz.Decision {
		if r == authz.ResourceCreditMemo {
			return authz.Decision{Allowed: true, Scope: scope}
		}
		return authz.Decision{}
	}
}

func listAll(t *testing.T, pool *pgxpool.Pool, check CheckFunc, c Caller, scope Scope) Page {
	t.Helper()
	p, err := List(context.Background(), pool, check, c, Params{Scope: scope, Limit: 100}, time.Now().UTC())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return p
}

func find(p Page, uuid string) (Item, bool) {
	for _, it := range p.Items {
		if it.RecordUUID == uuid {
			return it, true
		}
	}
	return Item{}, false
}
