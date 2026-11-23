//go:build dbtest

package vendorbill

import (
	"context"
	"math"
	"testing"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/dbtestutil"
	"stonesuite-backend/glpost"
	"stonesuite-backend/reports"
)

func near(a, b float64) bool { return math.Abs(a-b) < 0.005 }

// TestLedger_ApprovedBillPostsExpenseAndPayable: the bill's cost reaches the
// ledger as an expense (what the P&L reads) with the payable as its other side.
func TestLedger_ApprovedBillPostsExpenseAndPayable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	cogs := dbtestutil.SlotBalance(t, pool, glpost.SlotCOGS)

	bill := pendingBill(t, pool) // one 100.00 line; no approver, so already approved
	if live, _ := dbtestutil.LedgerEntries(t, pool, glpost.SourceVendorBill, bill.ID); live != 1 {
		t.Fatalf("entries = %d, want 1 (status %s)", live, bill.StatusCode)
	}
	lines := dbtestutil.EntryLines(t, pool, glpost.SourceVendorBill, bill.ID)
	if !near(lines[glpost.SlotCOGS], 100) || !near(lines[glpost.SlotAP], -100) {
		t.Errorf("entry = %v; want Dr COGS 100 / Cr AP 100", lines)
	}
	if got := dbtestutil.SlotBalance(t, pool, glpost.SlotCOGS) - cogs; !near(got, 100) {
		t.Errorf("COGS moved by %v, want 100", got)
	}

	if _, err := Transition(ctx, pool, bill.ID, "VOID", 1); err != nil {
		t.Fatalf("void: %v", err)
	}
	if live, rev := dbtestutil.LedgerEntries(t, pool, glpost.SourceVendorBill, bill.ID); live != 1 || rev != 1 {
		t.Errorf("after void: %d live, %d reversal; want 1, 1", live, rev)
	}
	if got := dbtestutil.SlotBalance(t, pool, glpost.SlotCOGS) - cogs; !near(got, 0) {
		t.Errorf("COGS after void moved by %v, want 0", got)
	}
}

func TestLedger_BillPostsOnApprovalNotBefore(t *testing.T) {
	pool := testPool(t)
	configureApprover(t, pool)
	bill := pendingBill(t, pool)
	if bill.StatusCode != "PAPV" {
		t.Fatalf("status = %q, want PAPV", bill.StatusCode)
	}
	if live, _ := dbtestutil.LedgerEntries(t, pool, glpost.SourceVendorBill, bill.ID); live != 0 {
		t.Fatalf("a bill awaiting approval has %d entries; want none", live)
	}
	if _, err := Approve(context.Background(), pool, bill.ID, 1, false); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if live, _ := dbtestutil.LedgerEntries(t, pool, glpost.SourceVendorBill, bill.ID); live != 1 {
		t.Errorf("entries after approval = %d, want 1", live)
	}
}

// TestReports_APAgingBucketsFollowTheTenantDateAndTotalsMatch guards the report
// fixes: "today" is the date passed in (the tenant's local day), not the
// database's CURRENT_DATE, and the totals are summed in SQL over every vendor.
func TestReports_APAgingBucketsFollowTheTenantDateAndTotalsMatch(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bill := pendingBill(t, pool) // dated 2026-08-01, no due date, 100.00 owed

	rowFor := func(today time.Time) (reports.AgingRow, reports.AgingTotals) {
		t.Helper()
		rows, totals, err := reports.APAging(ctx, pool, authz.ScopeAll, "", today)
		if err != nil {
			t.Fatalf("APAging: %v", err)
		}
		var vendor string
		if err := pool.QueryRow(ctx, `SELECT v.vendor_uuid::text FROM vendor_bill vb JOIN vendor v ON v.vendor_id = vb.vendor_bill_vendor_id WHERE vb.vendor_bill_uuid = $1`, bill.ID).Scan(&vendor); err != nil {
			t.Fatalf("find bill vendor: %v", err)
		}
		var sum float64
		for _, r := range rows {
			sum += r.Total
			if r.VendorUUID == vendor {
				return r, totals
			}
		}
		t.Fatalf("vendor %s missing from AP aging (rows sum %v)", vendor, sum)
		return reports.AgingRow{}, totals
	}

	// On the bill date nothing is past due.
	row, _ := rowFor(time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))
	if !near(row.Buckets[0], 100) {
		t.Errorf("on the bill date buckets = %v; want 100 in Current", row.Buckets)
	}
	// 114 days later it is in Over 90 days.
	row, totals := rowFor(time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC))
	if !near(row.Buckets[4], 100) || !near(row.Buckets[0], 0) {
		t.Errorf("114 days on, buckets = %v; want 100 in Over 90 days", row.Buckets)
	}
	// Totals are computed over all vendors: each bucket total is at least this
	// vendor's, and the grand total equals the bucket sum.
	var bucketSum float64
	for _, b := range totals.Buckets {
		bucketSum += b
	}
	if !near(bucketSum, totals.Total) || totals.Total < 100 || totals.Bills < 1 {
		t.Errorf("totals = %+v; buckets must sum to the total", totals)
	}
}
