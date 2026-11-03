//go:build dbtest

package vendortransactions

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/dbtestutil"
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
		a.identityID, suffix+"@vendortx.test", "VendorTx "+label).Scan(&a.userID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1::uuid, 'VendorTx', $2, $3, 1) RETURNING employee_id`,
		a.userID, label, suffix+"@vendortx.test").Scan(&a.employee))

	roleID, err := authz.CreateRole(ctx, pool, "vendortx-"+suffix, "vendortx-"+suffix, "", grants)
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

// seedVendor inserts a live vendor organisation and returns its uuid and
// internal id. Cleanup runs after the documents' own (LIFO).
func seedVendor(t *testing.T, pool *pgxpool.Pool, name string) (string, int) {
	t.Helper()
	ctx := context.Background()
	var (
		uuid string
		id   int
	)
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR'),
		        (SELECT s.record_status_id FROM lkp_record_status s
		           JOIN lkp_record_type rt ON rt.record_type_id = s.record_status_record_type
		          WHERE rt.record_type_code = 'VNDR' AND s.record_status_code = 'ACT_'),
		        'Organization', $1)
		RETURNING vendor_uuid::text, vendor_id`,
		fmt.Sprintf("%s %d", name, time.Now().UnixNano())).Scan(&uuid, &id))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM vendor WHERE vendor_id = $1`, id) })
	return uuid, id
}

// docSpec names a document's table, column prefix and record-type code.
type docSpec struct{ table, prefix, typeCode string }

var docSpecs = map[string]docSpec{
	"requisition":    {"requisition", "requisition", "REQN"},
	"purchase_order": {"purchase_order", "purchase_order", "PORD"},
	"item_receipt":   {"item_receipt", "item_receipt", "IRCT"},
	"vendor_bill":    {"vendor_bill", "vendor_bill", "VBIL"},
	"vendor_payment": {"vendor_payment", "vendor_payment", "VPAY"},
	"vendor_credit":  {"vendor_credit", "vendor_credit", "VCRD"},
}

// docSeed describes one document to insert. Date and Due are yyyy-mm-dd.
type docSeed struct {
	key, status, date, due string
	total                  float64 // grand total, or amount for payments
	balance                float64 // bill balance due; unapplied amount for payments and credits
	owner                  int     // employee id; 0 = unowned (requisitions: the requester)
	createdAt              string  // optional created_at override, for ordering tests
}

// seeded is an inserted document.
type seeded struct {
	uuid string
	id   int
}

// seedDoc inserts one minimal document for the vendor and registers its cleanup.
func seedDoc(t *testing.T, pool *pgxpool.Pool, vendorID int, d docSeed) seeded {
	t.Helper()
	spec, ok := docSpecs[d.key]
	require.Truef(t, ok, "doc spec %q", d.key)
	p := spec.prefix
	ctx := context.Background()

	var cols, vals []string
	var args []any
	add := func(col, expr string, arg any) {
		cols = append(cols, col)
		if arg == nil {
			vals = append(vals, expr)
			return
		}
		args = append(args, arg)
		vals = append(vals, replaceQ(expr, len(args)))
	}
	add("record_type", "(SELECT record_type_id FROM lkp_record_type WHERE record_type_code = ?)", spec.typeCode)
	add(p+"_status", "(SELECT s.record_status_id FROM lkp_record_status s"+
		" JOIN lkp_record_type rt ON rt.record_type_id = s.record_status_record_type"+
		" WHERE rt.record_type_code = '"+spec.typeCode+"' AND s.record_status_code = ?)", d.status)
	add(p+"_vendor_id", "?", vendorID)
	add(p+"_number", "?", fmt.Sprintf("%s%d", spec.typeCode, seq.Add(1)+time.Now().UnixNano()%1e9))
	if d.createdAt != "" {
		add(p+"_created_at", "?::timestamp", d.createdAt)
	}
	if d.date != "" && d.key != "requisition" {
		add(p+"_date", "?::date", d.date)
	}
	if d.owner != 0 {
		ownerCol := p + "_owner_id"
		if d.key == "requisition" {
			ownerCol = "requisition_requested_by_id"
		}
		add(ownerCol, "?", d.owner)
	}
	switch d.key {
	case "requisition":
		if d.owner == 0 {
			// The requester is NOT NULL: borrow any employee.
			add("requisition_requested_by_id", "(SELECT employee_id FROM employee ORDER BY employee_id LIMIT 1)", nil)
		}
		add("requisition_estimated_total", "?", d.total)
	case "purchase_order":
		add("purchase_order_grand_total", "?", d.total)
		if d.due != "" {
			add("purchase_order_expected_date", "?::date", d.due)
		}
	case "item_receipt":
		dbtestutil.EnsureLocation(t, pool)
		add("warehouse_id", "?", 1)
		add("purchase_order_id", "?", seedParentPO(t, pool, vendorID).id)
	case "vendor_bill":
		add("vendor_bill_grand_total", "?", d.total)
		add("vendor_bill_balance_due", "?", d.balance)
		if d.due != "" {
			add("vendor_bill_due_date", "?::date", d.due)
		}
	case "vendor_payment":
		add("vendor_payment_amount", "?", d.total)
		add("vendor_payment_unapplied_amount", "?", d.balance)
		add("vendor_payment_method", "(SELECT payment_method_id FROM lkp_payment_method ORDER BY payment_method_id LIMIT 1)", nil)
	case "vendor_credit":
		add("vendor_credit_grand_total", "?", d.total)
		add("vendor_credit_unapplied_amount", "?", d.balance)
	}

	var s seeded
	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING %s_uuid::text, %s_id",
		spec.table, joinComma(cols), joinComma(vals), p, p)
	require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&s.uuid, &s.id))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, fmt.Sprintf("DELETE FROM %s WHERE %s_id = $1", spec.table, p), s.id)
	})
	return s
}

// seedParentPO inserts the purchase order an item receipt must reference. It
// belongs to a throwaway vendor so it never shows up in the vendor under test.
func seedParentPO(t *testing.T, pool *pgxpool.Pool, _ int) seeded {
	t.Helper()
	_, otherVendorID := seedVendor(t, pool, "ParentPO")
	return seedDoc(t, pool, otherVendorID, docSeed{key: "purchase_order", status: "APPV", total: 1})
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

// replaceQ swaps the single ? placeholder in expr for $n.
func replaceQ(expr string, n int) string {
	out := make([]byte, 0, len(expr)+2)
	for i := 0; i < len(expr); i++ {
		if expr[i] == '?' {
			out = append(out, fmt.Sprintf("$%d", n)...)
			continue
		}
		out = append(out, expr[i])
	}
	return string(out)
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out
}
