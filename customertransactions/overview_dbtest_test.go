//go:build dbtest

package customertransactions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

func typeKeys(ov Overview) map[string]bool {
	out := map[string]bool{}
	for _, ti := range ov.Types {
		out[ti.Type] = true
	}
	return out
}

func TestOverview_DB(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()

	full := seedActor(t, pool, "full", readAll()...)
	noInvoice := seedActor(t, pool, "noinvoice", readAll("invoice")...)
	invoiceOnly := seedActor(t, pool, "invonly", read(authz.ResourceInvoice, authz.ScopeAll))
	ownOnly := seedActor(t, pool, "own", read(authz.ResourceInvoice, authz.ScopeOwn))
	nobody := seedActor(t, pool, "nobody")

	custUUID, custID := seedCRM(t, pool, "CUST", "Overview customer")
	leadUUID, _ := seedCRM(t, pool, "LEAD", "Overview lead")

	// Today is 2026-10-31, so the 12-month window opens 2025-10-31.
	seedDoc(t, pool, custID, docSeed{key: "invoice", status: "SENT", date: "2026-09-01", due: "2026-10-01", total: 1000, balance: 400}) // open, overdue
	seedDoc(t, pool, custID, docSeed{key: "invoice", status: "PART", date: "2026-08-01", due: "2026-12-01", total: 600, balance: 100})  // open, not overdue
	seedDoc(t, pool, custID, docSeed{key: "invoice", status: "PAID", date: "2026-03-01", total: 800})                                   // billed
	seedDoc(t, pool, custID, docSeed{key: "invoice", status: "PAID", date: "2025-06-01", total: 700})                                   // billed, outside window
	seedDoc(t, pool, custID, docSeed{key: "invoice", status: "DRFT", date: "2026-09-01", total: 999})                                   // never sent
	seedDoc(t, pool, custID, docSeed{key: "payment", status: "APPV", date: "2026-06-01", total: 300, balance: 50})                      // unapplied 50, paid
	seedDoc(t, pool, custID, docSeed{key: "payment", status: "VOID", date: "2026-07-01", total: 200, balance: 200})                     // void: nothing
	seedDoc(t, pool, custID, docSeed{key: "payment", status: "DEPO", date: "2026-05-01", total: 150})                                   // paid
	seedDoc(t, pool, custID, docSeed{key: "payment", status: "APPV", date: "2025-01-01", total: 90})                                    // paid, outside window
	seedDoc(t, pool, custID, docSeed{key: "credit_memo", status: "APPV", date: "2026-04-01", total: 80, balance: 30})                   // unapplied 30
	seedDoc(t, pool, custID, docSeed{key: "credit_memo", status: "DRFT", date: "2026-04-02", total: 40, balance: 40})                   // draft: not usable

	overview := func(t *testing.T, who testActor) Overview {
		t.Helper()
		ov, err := GetOverview(ctx, pool, who.identityID, custUUID, dbToday)
		require.NoError(t, err)
		return ov
	}

	t.Run("figures match the seeded documents", func(t *testing.T) {
		ov := overview(t, full)
		s := ov.Summary
		require.NotNil(t, s.OpenBalance)
		assert.InDelta(t, 500, *s.OpenBalance, 0.001)
		assert.Equal(t, 2, *s.OpenInvoiceCount)
		assert.InDelta(t, 400, *s.OverdueBalance, 0.001)
		assert.Equal(t, 1, *s.OverdueInvoiceCount)
		assert.InDelta(t, 80, *s.UnappliedCredits, 0.001, "50 from the approved payment + 30 from the approved credit memo")
		assert.InDelta(t, 2400, *s.Billed12m, 0.001, "SENT + PART + the 2026 PAID invoice; draft and the 2025 invoice excluded")
		assert.InDelta(t, 450, *s.Paid12m, 0.001, "APPV 300 + DEPO 150; void and the 2025 payment excluded")
		assert.NotNil(t, s.LastActivityAt)
		assert.False(t, ov.ScopeLimited)
		assert.Len(t, ov.Types, len(Sources()))
	})

	t.Run("counts group by type and status", func(t *testing.T) {
		byType := map[string]Count{}
		for _, c := range overview(t, full).Counts {
			byType[c.Type] = c
		}
		inv := byType["invoice"]
		assert.Equal(t, 5, inv.Count)
		assert.Equal(t, 2, inv.OpenCount)
		statuses := map[string]int{}
		for _, sc := range inv.Statuses {
			statuses[sc.Code] = sc.Count
		}
		assert.Equal(t, map[string]int{"SENT": 1, "PART": 1, "PAID": 2, "DRFT": 1}, statuses)
		assert.Equal(t, 4, byType["payment"].Count)
		assert.Equal(t, 2, byType["credit_memo"].Count)
		assert.Equal(t, 1, byType["payment"].OpenCount, "only the approved payment with a balance is open")
		assert.NotContains(t, byType, "refund", "types with no records are omitted")
	})

	t.Run("AR fields are null when invoices are unreadable", func(t *testing.T) {
		ov := overview(t, noInvoice)
		s := ov.Summary
		assert.Nil(t, s.OpenBalance)
		assert.Nil(t, s.OpenInvoiceCount)
		assert.Nil(t, s.OverdueBalance)
		assert.Nil(t, s.OverdueInvoiceCount)
		assert.Nil(t, s.Billed12m)
		require.NotNil(t, s.UnappliedCredits)
		assert.InDelta(t, 80, *s.UnappliedCredits, 0.001)
		require.NotNil(t, s.Paid12m)
		assert.True(t, ov.ScopeLimited)
		assert.False(t, typeKeys(ov)["invoice"])
	})

	t.Run("credit fields are null when neither payments nor credit memos are readable", func(t *testing.T) {
		ov := overview(t, invoiceOnly)
		assert.Nil(t, ov.Summary.UnappliedCredits)
		assert.Nil(t, ov.Summary.Paid12m)
		require.NotNil(t, ov.Summary.OpenBalance)
		assert.True(t, ov.ScopeLimited)
	})

	t.Run("an own-scoped caller sees only records they own, flagged as limited", func(t *testing.T) {
		ov := overview(t, ownOnly)
		require.NotNil(t, ov.Summary.OpenBalance)
		assert.Zero(t, *ov.Summary.OpenBalance, "the seeded invoices are unowned")
		assert.Zero(t, *ov.Summary.OpenInvoiceCount)
		assert.True(t, ov.ScopeLimited)
		assert.Empty(t, ov.Counts)
	})

	t.Run("a caller who can read nothing gets an empty overview", func(t *testing.T) {
		ov := overview(t, nobody)
		assert.Empty(t, ov.Types)
		assert.Empty(t, ov.Counts)
		assert.Nil(t, ov.Summary.OpenBalance)
		assert.Nil(t, ov.Summary.LastActivityAt)
		assert.True(t, ov.ScopeLimited)
	})

	t.Run("lead and unknown uuids are not found", func(t *testing.T) {
		for _, id := range []string{leadUUID, "00000000-0000-0000-0000-000000000000"} {
			_, err := GetOverview(ctx, pool, full.identityID, id, dbToday)
			assert.ErrorIs(t, err, ErrCustomerNotFound, id)
		}
	})
}
