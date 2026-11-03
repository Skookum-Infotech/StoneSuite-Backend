//go:build dbtest

package vendortransactions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

func listAll(t *testing.T, a testActor, vendorUUID string, mod func(*Params)) []Row {
	t.Helper()
	pool := dbPool(t)
	p := Params{IdentityID: a.identityID, VendorUUID: vendorUUID, Limit: MaxLimit, Today: dbToday}
	if mod != nil {
		mod(&p)
	}
	page, err := List(context.Background(), pool, p)
	require.NoError(t, err)
	return page.Rows
}

func keysOf(rows []Row) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.Type]++
	}
	return out
}

// seedMixedVendor seeds one document of every type plus closed/voided variants.
func seedMixedVendor(t *testing.T, a testActor) (string, int) {
	t.Helper()
	pool := dbPool(t)
	vUUID, vID := seedVendor(t, pool, "Mixed")
	seedDoc(t, pool, vID, docSeed{key: "requisition", status: "APPV", total: 50, owner: a.employee})
	seedDoc(t, pool, vID, docSeed{key: "purchase_order", status: "SENT", date: "2026-10-01", total: 500, owner: a.employee})
	seedDoc(t, pool, vID, docSeed{key: "purchase_order", status: "CLSD", date: "2026-09-01", total: 300})
	seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-10-01", due: "2026-12-01", total: 400, balance: 400, owner: a.employee})
	seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "PART", date: "2026-09-01", due: "2026-10-01", total: 200, balance: 120})
	seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "PAID", date: "2026-08-01", due: "2026-09-01", total: 100, balance: 0})
	seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "VOID", date: "2026-08-01", due: "2026-09-01", total: 999, balance: 999})
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "SENT", date: "2026-10-10", total: 250, balance: 40})
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "VOID", date: "2026-10-11", total: 75, balance: 75})
	seedDoc(t, pool, vID, docSeed{key: "vendor_credit", status: "APPV", date: "2026-10-12", total: 60, balance: 60})
	seedDoc(t, pool, vID, docSeed{key: "item_receipt", status: "PEND", date: "2026-10-13"})
	return vUUID, vID
}

func TestList_OpenToggleExcludesVoidedAndClosed(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "open", readAll()...)
	vUUID, _ := seedMixedVendor(t, a)

	all := listAll(t, a, vUUID, nil)
	assert.Equal(t, map[string]int{
		"requisition": 1, "purchase_order": 2, "vendor_bill": 4, "vendor_payment": 2, "vendor_credit": 1, "item_receipt": 1,
	}, keysOf(all))

	open := listAll(t, a, vUUID, func(p *Params) { p.OpenOnly = true })
	for _, r := range open {
		assert.NotContains(t, []string{"VOID", "CLSD", "PAID", "CANC"}, r.StatusCode, "%s %s is not open", r.Type, r.Number)
		assert.True(t, r.IsOpen)
	}
	assert.Equal(t, map[string]int{
		"requisition": 1, "purchase_order": 1, "vendor_bill": 2, "vendor_payment": 1, "vendor_credit": 1, "item_receipt": 1,
	}, keysOf(open))
}

func TestList_OverdueFlagOnlyOnOpenPastDueBills(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "overdue", readAll()...)
	vUUID, _ := seedMixedVendor(t, a)
	for _, r := range listAll(t, a, vUUID, nil) {
		wantOverdue := r.Type == "vendor_bill" && r.StatusCode == "PART" // open, due 2026-10-01 < today
		assert.Equal(t, wantOverdue, r.IsOverdue, "%s %s", r.Type, r.StatusCode)
	}
}

func TestList_OnlyThisVendorAndNotDeleted(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "iso", readAll()...)
	mine, mineID := seedVendor(t, pool, "Mine")
	_, otherID := seedVendor(t, pool, "Other")
	seedDoc(t, pool, mineID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-10-01", total: 1, balance: 1})
	gone := seedDoc(t, pool, mineID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-10-01", total: 2, balance: 2})
	seedDoc(t, pool, otherID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-10-01", total: 3, balance: 3})
	softDelete(t, pool, "vendor_bill", gone, a.employee)

	rows := listAll(t, a, mine, nil)
	require.Len(t, rows, 1)
	assert.Equal(t, 1.0, *rows[0].Amount)
}

func TestList_ReadGrantPerModule(t *testing.T) {
	pool := dbPool(t)
	owner := seedActor(t, pool, "owner", readAll()...)
	vUUID, _ := seedMixedVendor(t, owner)

	t.Run("a module without :read never appears", func(t *testing.T) {
		a := seedActor(t, pool, "nobill", readAll("vendor_bill", "vendor_payment")...)
		got := keysOf(listAll(t, a, vUUID, nil))
		assert.NotContains(t, got, "vendor_bill")
		assert.NotContains(t, got, "vendor_payment")
		assert.Contains(t, got, "purchase_order")
	})

	t.Run("naming an unreadable type returns nothing and flags the denial", func(t *testing.T) {
		a := seedActor(t, pool, "named", readAll("vendor_bill")...)
		page, err := List(context.Background(), pool, Params{
			IdentityID: a.identityID, VendorUUID: vUUID, Type: "vendor_bill", Today: dbToday,
		})
		require.NoError(t, err)
		assert.Empty(t, page.Rows)
		assert.True(t, page.Denied)
	})

	t.Run("no module grants at all is an empty page", func(t *testing.T) {
		a := seedActor(t, pool, "none")
		assert.Empty(t, listAll(t, a, vUUID, nil))
	})

	t.Run("own scope sees only its own rows", func(t *testing.T) {
		grants := []authz.Grant{
			read(authz.ResourcePurchaseOrder, authz.ScopeOwn),
			read(authz.ResourceVendorBill, authz.ScopeOwn),
			read(authz.ResourceRequisition, authz.ScopeOwn),
		}
		a := seedActor(t, pool, "own", grants...)
		// seedMixedVendor attributed rows to `owner`; a fresh own-scoped actor owns none.
		assert.Empty(t, listAll(t, a, vUUID, nil))

		vU, vID := seedVendor(t, pool, "OwnVendor")
		seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-10-01", total: 5, balance: 5, owner: a.employee})
		seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-10-01", total: 6, balance: 6, owner: owner.employee})
		seedDoc(t, pool, vID, docSeed{key: "requisition", status: "APPV", total: 7, owner: a.employee})
		rows := listAll(t, a, vU, nil)
		assert.Equal(t, map[string]int{"vendor_bill": 1, "requisition": 1}, keysOf(rows))
	})
}

func TestList_FiltersAndSearch(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "filt", readAll()...)
	vUUID, _ := seedMixedVendor(t, a)

	byType := listAll(t, a, vUUID, func(p *Params) { p.Type = "vendor_bill" })
	assert.Equal(t, map[string]int{"vendor_bill": 4}, keysOf(byType))

	byStatus := listAll(t, a, vUUID, func(p *Params) { p.Type = "vendor_bill"; p.Status = "PART" })
	require.Len(t, byStatus, 1)

	byDate := listAll(t, a, vUUID, func(p *Params) {
		p.Type = "vendor_bill"
		p.From, p.To = mustDate(t, "2026-09-01"), mustDate(t, "2026-10-31")
	})
	assert.Len(t, byDate, 2, "APPV 10-01 and PART 09-01")

	bySearch := listAll(t, a, vUUID, func(p *Params) { p.Search = byType[0].Number })
	require.Len(t, bySearch, 1)
	assert.Equal(t, byType[0].ID, bySearch[0].ID)
}

func TestList_KeysetPagination(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "page", readAll()...)
	vUUID, vID := seedVendor(t, pool, "Paged")
	// Three bills share one created_at to force the uuid tiebreak, two more differ.
	for _, ts := range []string{"2026-10-01 10:00:00", "2026-10-01 10:00:00", "2026-10-01 10:00:00", "2026-10-02 10:00:00", "2026-09-30 10:00:00"} {
		seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "APPV", date: "2026-10-01", total: 1, balance: 1, createdAt: ts})
	}
	ctx := context.Background()

	var seen []string
	cursorTok := ""
	for pages := 0; pages < 10; pages++ {
		page, err := List(ctx, pool, Params{IdentityID: a.identityID, VendorUUID: vUUID, Limit: 2, Cursor: cursorTok, Today: dbToday})
		require.NoError(t, err)
		for _, r := range page.Rows {
			seen = append(seen, r.ID)
		}
		if page.NextCursor == "" {
			break
		}
		require.Len(t, page.Rows, 2, "only the last page may be short")
		cursorTok = page.NextCursor
	}
	assert.Len(t, seen, 5)
	uniq := map[string]bool{}
	for _, id := range seen {
		uniq[id] = true
	}
	assert.Len(t, uniq, 5, "no row repeats or is skipped across pages")

	full := listAll(t, a, vUUID, nil)
	require.Len(t, full, 5)
	for i, r := range full {
		assert.Equal(t, r.ID, seen[i], "paged order equals the unpaged order")
	}
}

func TestList_UnknownOrDeletedVendor(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "unk", readAll()...)
	ctx := context.Background()

	_, err := List(ctx, pool, Params{IdentityID: a.identityID, VendorUUID: "00000000-0000-0000-0000-000000000000", Today: dbToday})
	assert.ErrorIs(t, err, ErrVendorNotFound)
	_, err = GetOverview(ctx, pool, a.identityID, "00000000-0000-0000-0000-000000000000", dbToday)
	assert.ErrorIs(t, err, ErrVendorNotFound)

	vUUID, vID := seedVendor(t, pool, "Deleted")
	_, err = pool.Exec(ctx, `UPDATE vendor SET vendor_deleted_at = NOW(), vendor_deleted_by = $2 WHERE vendor_id = $1`, vID, a.employee)
	require.NoError(t, err)
	_, err = List(ctx, pool, Params{IdentityID: a.identityID, VendorUUID: vUUID, Today: dbToday})
	assert.ErrorIs(t, err, ErrVendorNotFound)
}
