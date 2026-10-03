//go:build dbtest

package mytransactions

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
)

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

// TestSourcesSQL_ExecutesAgainstSchema proves every registry fragment names a
// real table/column: each module's branch runs alone (own and all scope), and
// the full union runs too, so a typo'd column fails here rather than in prod.
func TestSourcesSQL_ExecutesAgainstSchema(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	userID := "00000000-0000-0000-0000-000000000000"

	for _, own := range []bool{false, true} {
		for _, s := range Sources() {
			t.Run(fmt.Sprintf("%s/own=%v", s.Key, own), func(t *testing.T) {
				sql, args := buildPageSQL([]granted{{Source: s, own: own}}, pageFilter{role: RoleAll, limit: 5}, 1, &userID)
				rows, err := pool.Query(ctx, sql, args...)
				require.NoError(t, err)
				rows.Close()
				require.NoError(t, rows.Err())
			})
		}
		all := allGranted(t, own)
		sql, args := buildPageSQL(all, pageFilter{role: RoleAll, search: "x", limit: 5, offset: 10}, 1, &userID)
		rows, err := pool.Query(ctx, sql, args...)
		require.NoError(t, err)
		rows.Close()
		require.NoError(t, rows.Err())

		countSQL, countArgs := buildCountSQL(all, pageFilter{role: RoleCreated, search: "x"}, 1, &userID)
		var n int
		require.NoError(t, pool.QueryRow(ctx, countSQL, countArgs...).Scan(&n))

		sumSQL, sumArgs := buildSummarySQL(all, time.Now().Add(-recentWindow), 1, &userID)
		var total, created, updated, recent int
		require.NoError(t, pool.QueryRow(ctx, sumSQL, sumArgs...).Scan(&total, &created, &updated, &recent))
	}
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
		a.identityID, suffix+"@mytx.test", "MyTx "+label).Scan(&a.userID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1::uuid, 'MyTx', $2, $3, 1) RETURNING employee_id`,
		a.userID, label, suffix+"@mytx.test").Scan(&a.employee))

	roleID, err := authz.CreateRole(ctx, pool, "mytx-"+suffix, "mytx-"+suffix, "", grants)
	require.NoError(t, err)
	require.NoError(t, authz.AssignRole(ctx, pool, a.userID, roleID))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM audit_logs WHERE actor_user_id = $1::uuid`, a.userID)
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

// seedCRM inserts a CRM record of the given stage code ("LEAD", "CUST") with an
// explicit updated_at, created by `creator` and owned by `owner` (0 = unowned).
func seedCRM(t *testing.T, pool *pgxpool.Pool, typeCode, name string, creator, owner int, updatedAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	var ownerArg any
	if owner != 0 {
		ownerArg = owner
	}
	var id string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by, customer_crm_owner_user_id, customer_updated_at)
		SELECT record_type_id, $2, $3, $4, $5::timestamp FROM lkp_record_type WHERE record_type_code = $1
		RETURNING customer_uuid::text`, typeCode, name, creator, ownerArg, updatedAt.UTC().Format(timestampLayout)).Scan(&id))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM customer WHERE customer_uuid = $1::uuid`, id)
	})
	return id
}

// seedItem inserts an inventory item with explicit creator/updater and updated_at.
func seedItem(t *testing.T, pool *pgxpool.Pool, sku string, creator, updater int, updatedAt time.Time) string {
	t.Helper()
	ctx := context.Background()
	var id string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price,
			inventory_item_created_by, inventory_item_updated_by, inventory_item_updated_at)
		VALUES ($1, $2, 1, 10.00, $3, $4, $5::timestamp) RETURNING inventory_item_uuid::text`,
		sku, "Item "+sku, creator, updater, updatedAt.UTC().Format(timestampLayout)).Scan(&id))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM inventory_item WHERE inventory_item_uuid = $1::uuid`, id)
	})
	return id
}

func auditUpdate(t *testing.T, pool *pgxpool.Pool, userID, recordID, action string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO audit_logs (actor_user_id, action, resource, resource_id) VALUES ($1::uuid, $2, 'lead', $3)`,
		userID, action, recordID)
	require.NoError(t, err)
}

func rowIDs(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func TestList_DB(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	at := func(minutes int) time.Time { return base.Add(time.Duration(minutes) * time.Minute) }

	me := seedActor(t, pool, "me",
		read(authz.ResourceLead, authz.ScopeAll),
		read(authz.ResourceCustomer, authz.ScopeOwn),
		read(authz.ResourceInventoryItem, authz.ScopeOwn))
	other := seedActor(t, pool, "other")

	tag := fmt.Sprintf("%d", time.Now().UnixNano())
	leadCreated := seedCRM(t, pool, "LEAD", "Created lead "+tag, me.employee, 0, at(1))
	leadAudited := seedCRM(t, pool, "LEAD", "Audited lead "+tag, other.employee, 0, at(2))
	auditUpdate(t, pool, me.userID, leadAudited, "update")
	_ = seedCRM(t, pool, "LEAD", "Untouched lead "+tag, other.employee, 0, at(3))
	leadDeleted := seedCRM(t, pool, "LEAD", "Deleted lead "+tag, me.employee, 0, at(4))
	_, err := pool.Exec(ctx, `UPDATE customer SET customer_deleted_at = NOW(), customer_deleted_by = 1 WHERE customer_uuid = $1::uuid`, leadDeleted)
	require.NoError(t, err)
	// Own-scoped customer read: only customers I also own are visible.
	custOwned := seedCRM(t, pool, "CUST", "Owned customer "+tag, me.employee, me.employee, at(5))
	_ = seedCRM(t, pool, "CUST", "Foreign customer "+tag, me.employee, other.employee, at(6))

	itemCreated := seedItem(t, pool, "MYTX-C-"+tag, me.employee, other.employee, at(7)) // I created, they edited later
	itemUpdated := seedItem(t, pool, "MYTX-U-"+tag, other.employee, me.employee, at(8)) // they created, I last updated
	_ = seedItem(t, pool, "MYTX-O-"+tag, other.employee, other.employee, at(9))         // nothing to do with me
	itemOwnOnly := seedItem(t, pool, "MYTX-Z-"+tag, me.employee, me.employee, at(10))   // mine on both counts

	// Every row this test seeded carries tag in its name or sku, so searching for
	// it isolates them from anything else in the shared database.
	list := func(t *testing.T, p Params) Page {
		t.Helper()
		p.IdentityID = me.identityID
		p.Search = tag
		page, err := List(ctx, pool, p)
		require.NoError(t, err)
		return page
	}

	wantAll := []string{itemOwnOnly, itemUpdated, itemCreated, custOwned, leadAudited, leadCreated}

	t.Run("lists what I created or updated, newest first, with the right role", func(t *testing.T) {
		page := list(t, Params{Limit: 50})
		require.Equal(t, wantAll, rowIDs(page.Rows))

		byID := map[string]Row{}
		for _, r := range page.Rows {
			byID[r.ID] = r
		}
		assert.Equal(t, RoleCreated, byID[leadCreated].Role)
		assert.Equal(t, RoleUpdated, byID[leadAudited].Role, "CRM 'updated by me' comes from the audit trail")
		assert.Equal(t, RoleCreated, byID[itemCreated].Role, "I created it; a later edit by someone else does not take it away")
		assert.Equal(t, RoleUpdated, byID[itemUpdated].Role)
		assert.Equal(t, RoleCreated, byID[custOwned].Role)

		lead := byID[leadCreated]
		assert.Equal(t, "lead", lead.Type)
		assert.Equal(t, "Lead", lead.TypeLabel)
		assert.Equal(t, "crm", lead.Domain)
		assert.Equal(t, "lead", lead.Module)
		assert.Equal(t, "Created lead "+tag, lead.Name)
		assert.Nil(t, lead.Amount)
		item := byID[itemCreated]
		assert.Equal(t, "inventory", item.Domain)
		assert.Equal(t, "item", item.Module)
		assert.Equal(t, "MYTX-C-"+tag, item.Number)
	})

	t.Run("hides soft-deleted, untouched and out-of-scope records", func(t *testing.T) {
		ids := rowIDs(list(t, Params{Limit: 50}).Rows)
		assert.NotContains(t, ids, leadDeleted)
		assert.Len(t, ids, len(wantAll), "untouched lead, foreign-owned customer and unrelated item are all absent")
	})

	t.Run("role filter", func(t *testing.T) {
		assert.Equal(t, []string{itemOwnOnly, itemCreated, custOwned, leadCreated}, rowIDs(list(t, Params{Role: RoleCreated, Limit: 50}).Rows))
		assert.Equal(t, []string{itemUpdated, leadAudited}, rowIDs(list(t, Params{Role: RoleUpdated, Limit: 50}).Rows))
	})

	t.Run("type filter", func(t *testing.T) {
		assert.Equal(t, []string{leadAudited, leadCreated}, rowIDs(list(t, Params{Type: "lead", Limit: 50}).Rows))
	})

	t.Run("numbered pages walk every row exactly once and report the total", func(t *testing.T) {
		var got []string
		for pageNo := 1; pageNo <= 3; pageNo++ {
			page := list(t, Params{Page: pageNo, Limit: 2})
			assert.Equal(t, len(wantAll), page.Total, "total counts every matching row, not just this page")
			assert.Equal(t, pageNo, page.Page)
			assert.Equal(t, 2, page.Limit)
			assert.Len(t, page.Rows, 2)
			got = append(got, rowIDs(page.Rows)...)
		}
		assert.Equal(t, wantAll, got)
	})

	t.Run("a short last page and an out-of-range page both report the true total", func(t *testing.T) {
		last := list(t, Params{Page: 2, Limit: 4})
		assert.Len(t, last.Rows, 2)
		assert.Equal(t, len(wantAll), last.Total)

		beyond := list(t, Params{Page: 9, Limit: 4})
		assert.Empty(t, beyond.Rows)
		assert.Equal(t, len(wantAll), beyond.Total, "so the client can step back to the last real page")
		assert.Equal(t, 9, beyond.Page)
	})

	t.Run("total follows the filters", func(t *testing.T) {
		assert.Equal(t, 4, list(t, Params{Role: RoleCreated, Limit: 1}).Total)
		assert.Equal(t, 2, list(t, Params{Role: RoleUpdated, Limit: 1}).Total)
		assert.Equal(t, 2, list(t, Params{Type: "lead", Limit: 1}).Total)
		assert.Zero(t, list(t, Params{Type: "invoice", Limit: 1}).Total)
	})

	t.Run("overview carries unfiltered summary and the readable types", func(t *testing.T) {
		ov, err := GetOverview(ctx, pool, me.identityID)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, ov.Summary.Total, len(wantAll))
		assert.Equal(t, ov.Summary.Total, ov.Summary.Created+ov.Summary.Updated)
		assert.GreaterOrEqual(t, ov.Summary.Recent, len(wantAll))
		keys := map[string]bool{}
		for _, ti := range ov.Types {
			keys[ti.Type] = true
		}
		assert.True(t, keys["lead"] && keys["customer"] && keys["inventory_item"])
		assert.False(t, keys["invoice"], "no invoice:read grant, so invoices are not even offered")
	})

	t.Run("a caller with no read grants gets ErrNoAccess", func(t *testing.T) {
		nobody := seedActor(t, pool, "nobody")
		_, err := List(ctx, pool, Params{IdentityID: nobody.identityID})
		assert.ErrorIs(t, err, ErrNoAccess)
		_, err = GetOverview(ctx, pool, nobody.identityID)
		assert.ErrorIs(t, err, ErrNoAccess)
	})

	t.Run("bad inputs are rejected before any query", func(t *testing.T) {
		var invalid *InvalidParamError
		_, err := List(ctx, pool, Params{IdentityID: me.identityID, Role: "bogus"})
		assert.ErrorAs(t, err, &invalid)
		_, err = List(ctx, pool, Params{IdentityID: me.identityID, Page: -1})
		assert.ErrorAs(t, err, &invalid)
	})

	t.Run("a type the caller cannot read returns nothing, not an error", func(t *testing.T) {
		page := list(t, Params{Type: "invoice", Limit: 50})
		assert.Empty(t, page.Rows)
	})
}
