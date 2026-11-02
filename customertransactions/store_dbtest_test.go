//go:build dbtest

package customertransactions

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

// TestBranchesExecuteAgainstSchema proves every registry fragment names a real
// table and column: each source's branch runs alone (own and all scope), the full
// union runs paged, counted and summarised, so a typo'd column fails here rather
// than in production.
func TestBranchesExecuteAgainstSchema(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	filtered := pageFilter{status: "SENT", from: mustDate(t, "2026-01-01"), to: mustDate(t, "2026-12-31"), search: "x", openOnly: true, limit: 5, offset: 10}

	for _, own := range []bool{false, true} {
		for _, s := range Sources() {
			t.Run(fmt.Sprintf("%s/own=%v", s.Key, own), func(t *testing.T) {
				sql, args := buildPageSQL([]granted{{Source: s, own: own}}, pageFilter{limit: 5}, 1, 1, dbToday)
				rows, err := pool.Query(ctx, sql, args...)
				require.NoError(t, err)
				rows.Close()
				require.NoError(t, rows.Err())
			})
		}
		all := allGranted(t, own)

		sql, args := buildPageSQL(all, filtered, 1, 1, dbToday)
		rows, err := pool.Query(ctx, sql, args...)
		require.NoError(t, err)
		rows.Close()
		require.NoError(t, rows.Err())

		sql, args = buildCountSQL(all, filtered, 1, 1, dbToday)
		var n int
		require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&n))

		inv := granted{Source: mustSource(t, "invoice"), own: own}
		sql, args = buildReceivablesSQL(inv, 1, 1, dbToday)
		var f1, f2, f3 float64
		var c1, c2 int
		require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&f1, &c1, &f2, &c2, &f3))

		credits := []granted{{Source: mustSource(t, "payment"), own: own}, {Source: mustSource(t, "credit_memo"), own: own}}
		sql, args = buildCreditsSQL(credits, 1, 1, dbToday)
		require.NoError(t, pool.QueryRow(ctx, sql, args...).Scan(&f1, &f2))

		sql, args = buildCountsSQL(all, 1, 1, dbToday)
		crows, err := pool.Query(ctx, sql, args...)
		require.NoError(t, err)
		crows.Close()
		require.NoError(t, crows.Err())
	}
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

	me := seedActor(t, pool, "me",
		read(authz.ResourceQuote, authz.ScopeAll),
		read(authz.ResourceInvoice, authz.ScopeAll),
		read(authz.ResourcePayment, authz.ScopeOwn),
		read(authz.ResourceSalesOrder, authz.ScopeOwn))
	other := seedActor(t, pool, "other")
	nobody := seedActor(t, pool, "nobody")

	custUUID, custID := seedCRM(t, pool, "CUST", "Customer A")
	otherUUID, otherID := seedCRM(t, pool, "CUST", "Customer B")
	leadUUID, _ := seedCRM(t, pool, "LEAD", "A lead")

	q1 := seedDoc(t, pool, custID, docSeed{key: "quote", status: "DRFT", date: "2026-01-10", total: 100, owner: me.employee})
	inv1 := seedDoc(t, pool, custID, docSeed{key: "invoice", status: "SENT", date: "2026-01-20", due: "2026-02-01", total: 1000, balance: 400, owner: other.employee})
	so1 := seedDoc(t, pool, custID, docSeed{key: "sales_order", status: "OPEN", date: "2026-02-15", total: 250, owner: me.employee})
	q2 := seedDoc(t, pool, custID, docSeed{key: "quote", status: "CANC", date: "2026-03-10", total: 80, owner: other.employee})
	inv2 := seedDoc(t, pool, custID, docSeed{key: "invoice", status: "PAID", date: "2026-05-01", total: 500, owner: me.employee})
	pay1 := seedDoc(t, pool, custID, docSeed{key: "payment", status: "APPV", date: "2026-06-01", total: 300, balance: 50, owner: me.employee})

	// Never visible to "me".
	qDeleted := seedDoc(t, pool, custID, docSeed{key: "quote", status: "DRFT", date: "2026-04-01", total: 1})
	softDelete(t, pool, "quote", qDeleted, me.employee)
	seedDoc(t, pool, custID, docSeed{key: "payment", status: "APPV", date: "2026-06-02", total: 9, balance: 9, owner: other.employee})
	seedDoc(t, pool, custID, docSeed{key: "sales_order", status: "OPEN", date: "2026-02-16", total: 9, owner: other.employee})
	seedDoc(t, pool, custID, docSeed{key: "refund", status: "PEND", date: "2026-07-01", total: 9})
	invB := seedDoc(t, pool, otherID, docSeed{key: "invoice", status: "SENT", date: "2026-08-01", due: "2026-09-01", total: 7, balance: 7})

	list := func(t *testing.T, p Params) Page {
		t.Helper()
		p.IdentityID, p.CustomerUUID, p.Today = me.identityID, custUUID, dbToday
		page, err := List(ctx, pool, p)
		require.NoError(t, err)
		return page
	}
	wantAll := []string{pay1.uuid, inv2.uuid, q2.uuid, so1.uuid, inv1.uuid, q1.uuid}

	t.Run("lists only this customer's readable, in-scope, live records newest first", func(t *testing.T) {
		page := list(t, Params{Limit: 50})
		require.Equal(t, wantAll, rowIDs(page.Rows))
		assert.Equal(t, len(wantAll), page.Total)
		assert.NotContains(t, rowIDs(page.Rows), invB.uuid)
		assert.NotContains(t, rowIDs(page.Rows), qDeleted.uuid)
	})

	t.Run("row fields", func(t *testing.T) {
		byID := map[string]Row{}
		for _, r := range list(t, Params{Limit: 50}).Rows {
			byID[r.ID] = r
		}
		inv := byID[inv1.uuid]
		assert.Equal(t, "invoice", inv.Type)
		assert.Equal(t, "Invoice", inv.TypeLabel)
		assert.Equal(t, "sales", inv.Domain)
		assert.Equal(t, "invoice", inv.Module)
		assert.Equal(t, "SENT", inv.StatusCode)
		assert.Equal(t, "2026-01-20", inv.Date.String())
		require.NotNil(t, inv.DueDate)
		assert.Equal(t, "2026-02-01", inv.DueDate.String())
		require.NotNil(t, inv.Amount)
		assert.InDelta(t, 1000, *inv.Amount, 0.001)
		require.NotNil(t, inv.Balance)
		assert.InDelta(t, 400, *inv.Balance, 0.001)
		assert.True(t, inv.IsOpen)
		assert.True(t, inv.IsOverdue, "past due and open on 2026-10-31")
		assert.False(t, byID[inv2.uuid].IsOpen)
		assert.False(t, byID[inv2.uuid].IsOverdue)
		assert.Nil(t, byID[q1.uuid].Balance, "a quote has no balance")
		require.NotNil(t, byID[pay1.uuid].Balance)
		assert.InDelta(t, 50, *byID[pay1.uuid].Balance, 0.001, "payment balance is the unapplied remainder")
	})

	t.Run("overdue follows the supplied today", func(t *testing.T) {
		p := Params{IdentityID: me.identityID, CustomerUUID: custUUID, Type: "invoice", Today: dbToday.AddDate(0, -10, 0)}
		page, err := List(ctx, pool, p)
		require.NoError(t, err)
		for _, r := range page.Rows {
			assert.False(t, r.IsOverdue, "a Today of 2025-12-31 is before the 2026-02-01 due date")
		}
	})

	t.Run("filters", func(t *testing.T) {
		assert.Equal(t, []string{inv2.uuid, inv1.uuid}, rowIDs(list(t, Params{Type: "invoice", Limit: 50}).Rows))
		assert.Equal(t, []string{inv1.uuid}, rowIDs(list(t, Params{Status: "SENT", Limit: 50}).Rows))
		assert.Equal(t, []string{pay1.uuid, so1.uuid, inv1.uuid, q1.uuid}, rowIDs(list(t, Params{OpenOnly: true, Limit: 50}).Rows))
		from, to := mustDate(t, "2026-02-01"), mustDate(t, "2026-03-31")
		assert.Equal(t, []string{q2.uuid, so1.uuid}, rowIDs(list(t, Params{From: from, To: to, Limit: 50}).Rows))
		inv1Number := list(t, Params{Type: "invoice", Status: "SENT"}).Rows[0].Number
		assert.Equal(t, []string{inv1.uuid}, rowIDs(list(t, Params{Search: inv1Number[1:], Limit: 50}).Rows))
		assert.Equal(t, []string{inv1.uuid}, rowIDs(list(t, Params{Type: "invoice", OpenOnly: true, Status: "SENT", From: mustDate(t, "2026-01-01")}).Rows))
	})

	t.Run("a type the caller cannot read returns nothing, not an error", func(t *testing.T) {
		page := list(t, Params{Type: "refund", Limit: 50})
		assert.Empty(t, page.Rows)
		assert.Zero(t, page.Total)
	})

	t.Run("numbered pages walk every row once; past the end keeps the true total", func(t *testing.T) {
		var got []string
		for pageNo := 1; pageNo <= 3; pageNo++ {
			page := list(t, Params{Page: pageNo, Limit: 2})
			assert.Equal(t, len(wantAll), page.Total)
			assert.Len(t, page.Rows, 2)
			got = append(got, rowIDs(page.Rows)...)
		}
		assert.Equal(t, wantAll, got)
		beyond := list(t, Params{Page: 9, Limit: 4})
		assert.Empty(t, beyond.Rows)
		assert.Equal(t, len(wantAll), beyond.Total)
		assert.Equal(t, 9, beyond.Page)
	})

	t.Run("another customer's feed has only its own records", func(t *testing.T) {
		page, err := List(ctx, pool, Params{IdentityID: me.identityID, CustomerUUID: otherUUID, Today: dbToday})
		require.NoError(t, err)
		assert.Equal(t, []string{invB.uuid}, rowIDs(page.Rows))
	})

	t.Run("a caller with no read grants gets an empty page", func(t *testing.T) {
		page, err := List(ctx, pool, Params{IdentityID: nobody.identityID, CustomerUUID: custUUID, Today: dbToday})
		require.NoError(t, err)
		assert.Empty(t, page.Rows)
		assert.Zero(t, page.Total)
	})

	t.Run("lead, unknown and malformed uuids are not found", func(t *testing.T) {
		for _, id := range []string{leadUUID, "00000000-0000-0000-0000-000000000000", "nope"} {
			_, err := List(ctx, pool, Params{IdentityID: me.identityID, CustomerUUID: id, Today: dbToday})
			assert.ErrorIs(t, err, ErrCustomerNotFound, id)
		}
	})

	t.Run("bad inputs are rejected before any query", func(t *testing.T) {
		var invalid *InvalidParamError
		_, err := List(ctx, pool, Params{IdentityID: me.identityID, CustomerUUID: custUUID, Status: "bad!"})
		assert.ErrorAs(t, err, &invalid)
		_, err = List(ctx, pool, Params{IdentityID: me.identityID, CustomerUUID: "nope", Page: -1})
		assert.ErrorAs(t, err, &invalid, "validation runs before the customer lookup")
	})
}
