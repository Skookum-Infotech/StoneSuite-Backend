//go:build dbtest

package customertransactions

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

// dbToday is the tenant-local "today" every DB test evaluates overdue against.
var dbToday = time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)

// seq makes document numbers unique across tests sharing one database.
var seq atomic.Int64

func dbPool(t *testing.T) *pgxpool.Pool {
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

// testActor is a seeded tenant user with an employee profile and an RBAC role.
type testActor struct {
	identityID string
	userID     string
	employee   int
}

func seedActor(t *testing.T, pool *pgxpool.Pool, label string, grants ...authz.Grant) testActor {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%s-%d", label, time.Now().UnixNano())
	var a testActor
	require.NoError(t, pool.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&a.identityID))
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO users (identity_id, email, full_name) VALUES ($1::uuid, $2, $3) RETURNING id::text`,
		a.identityID, suffix+"@custtx.test", "CustTx "+label).Scan(&a.userID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1::uuid, 'CustTx', $2, $3, 1) RETURNING employee_id`,
		a.userID, label, suffix+"@custtx.test").Scan(&a.employee))

	roleID, err := authz.CreateRole(ctx, pool, "custtx-"+suffix, "custtx-"+suffix, "", grants)
	require.NoError(t, err)
	require.NoError(t, authz.AssignRole(ctx, pool, a.userID, roleID))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM user_roles WHERE user_id = $1::uuid`, a.userID)
		_, _ = pool.Exec(ctx, `DELETE FROM roles WHERE id = $1::uuid`, roleID)
		_, _ = pool.Exec(ctx, `DELETE FROM employee WHERE employee_id = $1`, a.employee)
		_, _ = pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, a.userID)
	})
	return a
}

func read(res authz.Resource, scope authz.Scope) authz.Grant {
	return authz.Grant{Resource: res, Action: authz.ActionRead, Scope: scope}
}

// readAll grants read (all scope) on every registered source except skip.
func readAll(skip ...string) []authz.Grant {
	var out []authz.Grant
next:
	for _, s := range Sources() {
		for _, k := range skip {
			if s.Key == k {
				continue next
			}
		}
		out = append(out, read(s.Resource, authz.ScopeAll))
	}
	return out
}

// seedCRM inserts a CRM record of the given stage code ("LEAD", "CUST") and
// returns its uuid and internal id.
func seedCRM(t *testing.T, pool *pgxpool.Pool, typeCode, name string) (string, int) {
	t.Helper()
	ctx := context.Background()
	var (
		uuid string
		id   int
	)
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		SELECT record_type_id, $2, 1 FROM lkp_record_type WHERE record_type_code = $1
		RETURNING customer_uuid::text, customer_id`, typeCode, name).Scan(&uuid, &id))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM customer WHERE customer_id = $1`, id)
	})
	return uuid, id
}

// docSpec names a document's record-type code and column prefix.
type docSpec struct{ table, prefix, typeCode string }

var docSpecs = map[string]docSpec{
	"quote":           {"quote", "quote", "QUOT"},
	"estimate":        {"estimate", "estimate", "ESTM"},
	"sales_order":     {"sales_order", "sales_order", "SORD"},
	"fabrication_job": {"fabrication_job", "fabrication_job", "FJOB"},
	"invoice":         {"invoice", "invoice", "INVC"},
	"payment":         {"payment", "payment", "PYMT"},
	"credit_memo":     {"credit_memo", "credit_memo", "CRDT"},
	"refund":          {"refund", "refund", "RFND"},
}

// docSeed describes one document to insert. Date and Due are yyyy-mm-dd.
type docSeed struct {
	key, status, date, due string
	total                  float64 // grand total, or amount for payments and refunds
	balance                float64 // invoice balance due; unapplied amount for payments and credit memos
	owner                  int     // 0 = unowned
	salesOrderID           int     // fabrication jobs only
}

// seeded is an inserted document.
type seeded struct {
	uuid string
	id   int
}

// seedDoc inserts one minimal document for the customer and registers its cleanup.
func seedDoc(t *testing.T, pool *pgxpool.Pool, customerID int, d docSeed) seeded {
	t.Helper()
	spec, ok := docSpecs[d.key]
	require.Truef(t, ok, "doc spec %q", d.key)
	p := spec.prefix

	var cols, vals []string
	var args []any
	add := func(col, expr string, arg any) {
		cols = append(cols, col)
		if arg == nil {
			vals = append(vals, expr)
			return
		}
		args = append(args, arg)
		vals = append(vals, strings.ReplaceAll(expr, "?", fmt.Sprintf("$%d", len(args))))
	}
	add("record_type", "(SELECT record_type_id FROM lkp_record_type WHERE record_type_code = ?)", spec.typeCode)
	add(p+"_status", "(SELECT s.record_status_id FROM lkp_record_status s"+
		" JOIN lkp_record_type rt ON rt.record_type_id = s.record_status_record_type"+
		" WHERE rt.record_type_code = '"+spec.typeCode+"' AND s.record_status_code = ?)", d.status)
	add(p+"_customer_id", "?", customerID)
	add(p+"_number", "?", fmt.Sprintf("%s%d", strings.ToUpper(p[:2]), seq.Add(1)+time.Now().UnixNano()%1e9))
	if d.date != "" {
		add(p+"_date", "?::date", d.date)
	}
	if d.owner != 0 {
		ownerCol := p + "_owner_id"
		if d.key == "fabrication_job" {
			ownerCol = "job_owner_id"
		}
		add(ownerCol, "?", d.owner)
	}
	switch d.key {
	case "quote", "estimate":
		add(p+"_grand_total", "?", d.total)
		if d.due != "" {
			add(p+"_valid_until", "?::date", d.due)
		}
	case "sales_order":
		add(p+"_grand_total", "?", d.total)
	case "fabrication_job":
		add("sales_order_id", "?", d.salesOrderID)
		if d.due != "" {
			add("job_promised_install_date", "?::date", d.due)
		}
	case "invoice":
		add("invoice_grand_total", "?", d.total)
		add("invoice_balance_due", "?", d.balance)
		if d.due != "" {
			add("invoice_due_date", "?::date", d.due)
		}
	case "payment":
		add("payment_amount", "?", d.total)
		add("payment_unapplied_amount", "?", d.balance)
		add("payment_method", "(SELECT payment_method_id FROM lkp_payment_method ORDER BY payment_method_id LIMIT 1)", nil)
	case "credit_memo":
		add("credit_memo_grand_total", "?", d.total)
		add("credit_memo_unapplied_amount", "?", d.balance)
	case "refund":
		add("refund_amount", "?", d.total)
		add("refund_method", "(SELECT payment_method_id FROM lkp_payment_method ORDER BY payment_method_id LIMIT 1)", nil)
	}

	var s seeded
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s_uuid::text, %s_id",
		spec.table, strings.Join(cols, ", "), strings.Join(vals, ", "), p, p)
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&s.uuid, &s.id))
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), fmt.Sprintf("DELETE FROM %s WHERE %s_id = $1", spec.table, p), s.id)
	})
	return s
}

// softDelete marks a seeded document deleted, as the modules' own delete does.
func softDelete(t *testing.T, pool *pgxpool.Pool, key string, s seeded, byEmployee int) {
	t.Helper()
	p := docSpecs[key].prefix
	_, err := pool.Exec(context.Background(), fmt.Sprintf(
		"UPDATE %s SET %s_deleted_at = NOW(), %s_deleted_by = $2 WHERE %s_id = $1", docSpecs[key].table, p, p, p),
		s.id, byEmployee)
	require.NoError(t, err)
}
