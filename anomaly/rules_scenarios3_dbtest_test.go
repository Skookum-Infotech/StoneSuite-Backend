//go:build dbtest

package anomaly

import (
	"testing"
)

func TestScenarioScheduleActivityOverdue(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	activity := func(soID int, status string, daysAgo int) {
		completed := "NULL"
		if status == "completed" {
			completed = "now()"
		}
		f.insert("sales_order_schedule", "sales_order_schedule_id", `
			INSERT INTO sales_order_schedule (sales_order_id, activity_code, activity_sequence, schedule_status, scheduled_at, completed_at)
			VALUES ($1, 'templating', 1, $2, $3::date, `+completed+`) RETURNING sales_order_schedule_id`, soID, status, f.daysAgo(daysAgo))
	}
	late, lateUUID := f.salesOrder(cust, "OPEN", "10")
	activity(late, "pending", 10)
	recent, recentUUID := f.salesOrder(cust, "OPEN", "10")
	activity(recent, "pending", 1)
	done, doneUUID := f.salesOrder(cust, "OPEN", "10")
	activity(done, "completed", 10)
	got := f.run(RuleScheduleOverdue)
	month := f.today.Format("2006-01")
	flagged(t, got, lateUUID+":"+month, "an unfinished activity due 10 days ago")
	notFlagged(t, got, recentUUID+":"+month, "only a day late")
	notFlagged(t, got, doneUUID+":"+month, "the activity is completed")
}

func TestScenarioStockLedgerMismatch(t *testing.T) {
	f := newFixtures(t)
	movement := func(item int, delta string) {
		f.insert("inventory_ledger", "inventory_ledger_id", `
			INSERT INTO inventory_ledger (inventory_item_id, warehouse_id, event, quantity_delta)
			VALUES ($1, 1, 'received', $2::numeric) RETURNING inventory_ledger_id`, item, delta)
	}
	off, agrees := f.item("10"), f.item("10")
	f.stock(off, "50")
	movement(off, "30") // the balance says 50 but the movements add up to 30
	f.stock(agrees, "40")
	movement(agrees, "40")
	got := f.run(RuleStockLedgerMismatch)
	month := f.today.Format("2006-01")
	flagged(t, got, f.itemUUID(off)+":1:"+month, "balance 50 against movements of 30")
	notFlagged(t, got, f.itemUUID(agrees)+":1:"+month, "balance equals the movements")
}

// The only discounted line in the workspace must still be flagged: the usual
// discount is judged from the other orders, not from the order under test.
func TestScenarioHighDiscountWhenItIsTheOnlyDiscount(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	plain, plainUUID := f.salesOrder(cust, "DRFT", "100")
	f.soLine(plain, 1, "0", "10", nil)
	odd, oddUUID := f.salesOrder(cust, "DRFT", "100")
	f.soLine(odd, 1, "60", "10", nil)
	got := f.run(RuleHighDiscount)
	flagged(t, got, oddUUID, "the only discount in the workspace is 60%")
	notFlagged(t, got, plainUUID, "no discount")
}

// A second 60% order must be flagged even though another 60% order exists: one
// other discounted line is not a baseline, so the plain floor applies.
func TestScenarioHighDiscountWithTooLittleHistory(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	first, firstUUID := f.salesOrder(cust, "DRFT", "100")
	f.soLine(first, 1, "60", "10", nil)
	second, secondUUID := f.salesOrder(cust, "DRFT", "100")
	f.soLine(second, 1, "60", "10", nil)
	got := f.run(RuleHighDiscount)
	flagged(t, got, firstUUID, "60% with no real baseline")
	flagged(t, got, secondUUID, "60% with no real baseline")
}

// With an established norm the bar rises with it, but a very deep discount is
// always flagged: three times a 40% norm would otherwise be out of reach.
func TestScenarioHighDiscountWithEstablishedNorm(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	var normUUID string
	for i := 0; i <= discountBaselineMinLines; i++ { // the order under test plus enough others to set a norm
		so, u := f.salesOrder(cust, "DRFT", "100")
		f.soLine(so, 1, "40", "10", nil)
		normUUID = u
	}
	deep, deepUUID := f.salesOrder(cust, "DRFT", "100")
	f.soLine(deep, 1, "85", "10", nil)
	got := f.run(RuleHighDiscount)
	notFlagged(t, got, normUUID, "40% is this workspace's usual")
	flagged(t, got, deepUUID, "85% is always flagged")
}
