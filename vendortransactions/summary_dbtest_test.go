//go:build dbtest

package vendortransactions

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

// TestSummary_EqualsListRows pins the contract that the summary figures are
// derived from exactly the rows the list returns (same scope, same flags).
func TestSummary_EqualsListRows(t *testing.T) {
	pool := dbPool(t)
	for _, tc := range []struct {
		name   string
		grants func() []authz.Grant
	}{
		{"all modules", func() []authz.Grant { return readAll() }},
		{"no payments", func() []authz.Grant { return readAll("vendor_payment") }},
		{"no bills", func() []authz.Grant { return readAll("vendor_bill") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := seedActor(t, pool, "sum", tc.grants()...)
			vUUID, _ := seedMixedVendor(t, a)
			rows := listAll(t, a, vUUID, nil)
			ov, err := GetOverview(context.Background(), pool, a.identityID, vUUID, dbToday)
			require.NoError(t, err)

			var open, overdue, credits float64
			var openN, overdueN int
			total, openCount := map[string]int{}, map[string]int{}
			for _, r := range rows {
				total[r.Type]++
				if r.IsOpen {
					openCount[r.Type]++
				}
				if r.Balance == nil {
					continue
				}
				if r.Type == "vendor_bill" {
					if r.IsOpen {
						open += *r.Balance
						openN++
					}
					if r.IsOverdue {
						overdue += *r.Balance
						overdueN++
					}
				} else if r.IsOpen {
					credits += *r.Balance
				}
			}

			if _, ok := total["vendor_bill"]; !ok && tc.name == "no bills" {
				assert.Nil(t, ov.Summary.OpenBalance, "an unreadable module yields no figure")
			} else {
				require.NotNil(t, ov.Summary.OpenBalance)
				assert.InDelta(t, open, *ov.Summary.OpenBalance, 0.001)
				assert.Equal(t, openN, *ov.Summary.OpenBillCount)
				assert.InDelta(t, overdue, *ov.Summary.OverdueAmount, 0.001)
				assert.Equal(t, overdueN, *ov.Summary.OverdueBillCount)
			}
			require.NotNil(t, ov.Summary.UnappliedCredits)
			assert.InDelta(t, credits, *ov.Summary.UnappliedCredits, 0.001)

			for _, c := range ov.Counts {
				assert.Equal(t, total[c.Type], c.Count, c.Type)
				assert.Equal(t, openCount[c.Type], c.OpenCount, c.Type)
			}
			assert.Len(t, ov.Counts, len(total))
			assert.Len(t, ov.Types, len(typeInfosFor(tc.grants())))
			if len(rows) > 0 {
				assert.NotNil(t, ov.Summary.LastActivityAt)
			}
		})
	}
}

func typeInfosFor(grants []authz.Grant) []string {
	var out []string
	for _, s := range Sources() {
		for _, g := range grants {
			if g.Resource == s.Resource {
				out = append(out, s.Key)
				break
			}
		}
	}
	return out
}

func TestSummary_PaidTwelveMonthsCountsOnlySentPaymentsInWindow(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "paid", readAll()...)
	vUUID, vID := seedVendor(t, pool, "Paid")
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "SENT", date: "2026-09-01", total: 100, balance: 0})
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "SENT", date: "2025-01-01", total: 900, balance: 0}) // outside window
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "VOID", date: "2026-09-02", total: 800, balance: 0})
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "DRFT", date: "2026-09-02", total: 600, balance: 600}) // not yet committed
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "APPV", date: "2026-09-03", total: 700, balance: 0})   // approved counts

	ov, err := GetOverview(context.Background(), pool, a.identityID, vUUID, dbToday)
	require.NoError(t, err)
	require.NotNil(t, ov.Summary.Paid12m)
	assert.InDelta(t, 800, *ov.Summary.Paid12m, 0.001, "SENT 100 + APPV 700; DRFT, VOID and out-of-window excluded")

	// A settlement recorded directly on a bill adds to the total; a deleted one
	// and one older than the window do not.
	ctx := context.Background()
	bill := seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "PART", date: "2026-09-01", due: "2026-12-01", total: 500, balance: 300})
	for _, p := range []struct {
		amt     float64
		paidAt  string
		deleted bool
	}{{200, "2026-09-10", false}, {50, "2026-09-11", true}, {999, "2025-01-01", false}} {
		var id int
		require.NoError(t, pool.QueryRow(ctx, `
			INSERT INTO vendor_bill_payment (vendor_bill_id, amount, paid_at, deleted_at)
			VALUES ($1, $2, $3::date, CASE WHEN $4 THEN NOW() END) RETURNING vendor_bill_payment_id`,
			bill.id, p.amt, p.paidAt, p.deleted).Scan(&id))
		t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM vendor_bill_payment WHERE vendor_bill_payment_id = $1`, id) })
	}
	ov, err = GetOverview(ctx, pool, a.identityID, vUUID, dbToday)
	require.NoError(t, err)
	assert.InDelta(t, 1000, *ov.Summary.Paid12m, 0.001, "800 from payment documents + 200 direct bill payment")

	// Without bill read the direct settlement is not visible.
	nb := seedActor(t, pool, "paidnb", readAll("vendor_bill")...)
	ov, err = GetOverview(ctx, pool, nb.identityID, vUUID, dbToday)
	require.NoError(t, err)
	assert.InDelta(t, 800, *ov.Summary.Paid12m, 0.001)
}

func TestSummary_OverdueByStatusWithoutDueDate(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "odue", readAll()...)
	vUUID, vID := seedVendor(t, pool, "OdueNoDate")
	seedDoc(t, pool, vID, docSeed{key: "vendor_bill", status: "ODUE", date: "2026-09-01", total: 80, balance: 80}) // no due date
	ov, err := GetOverview(context.Background(), pool, a.identityID, vUUID, dbToday)
	require.NoError(t, err)
	require.NotNil(t, ov.Summary.OverdueAmount)
	assert.InDelta(t, 80, *ov.Summary.OverdueAmount, 0.001)
	assert.Equal(t, 1, *ov.Summary.OverdueBillCount)
}

func TestSummary_UnappliedCreditsFollowApplyRules(t *testing.T) {
	pool := dbPool(t)
	a := seedActor(t, pool, "draftpay", readAll()...)
	vUUID, vID := seedVendor(t, pool, "DraftPay")
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "DRFT", date: "2026-10-01", total: 90, balance: 90})
	seedDoc(t, pool, vID, docSeed{key: "vendor_payment", status: "VOID", date: "2026-10-01", total: 70, balance: 70})
	seedDoc(t, pool, vID, docSeed{key: "vendor_credit", status: "APPL", date: "2026-10-01", total: 60, balance: 0})
	ov, err := GetOverview(context.Background(), pool, a.identityID, vUUID, dbToday)
	require.NoError(t, err)
	assert.InDelta(t, 90, *ov.Summary.UnappliedCredits, 0.001, "non-void payment counts (AR convention); void and fully applied credit do not")
}
