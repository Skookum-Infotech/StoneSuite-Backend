//go:build dbtest

package anomaly

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Scenarios for the rules that the first batch did not cover: same shape, one
// record that should trip the rule and a control that should not.

// employee creates a user and an employee linked to it (the owner or actor of a record).
func (f *fixtures) employee() int {
	f.t.Helper()
	identity := newUUID()
	var userID string
	_, err := f.pool.Exec(context.Background(), `
		INSERT INTO users (identity_id, email, full_name, status) VALUES ($1::uuid, $2, 'Scenario User', 'active')`,
		identity, fmt.Sprintf("scn-%s@example.test", identity))
	require.NoError(f.t, err)
	f.undo = append(f.undo, fmt.Sprintf("DELETE FROM users WHERE identity_id = '%s'::uuid", identity))
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT id::text FROM users WHERE identity_id = $1::uuid`, identity).Scan(&userID))
	return f.insert("employee", "employee_id", `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1::uuid, 'Scn', 'User', $2, 1) RETURNING employee_id`, userID, fmt.Sprintf("scn-%s@example.test", identity))
}

func (f *fixtures) soLineID(soID int, itemID any) int {
	return f.insert("sales_order_item", "sales_order_item_id", `
		INSERT INTO sales_order_item (sales_order_id, line_number, discount_percent, unit_price, inventory_item_id)
		VALUES ($1, $3, 0, 10, $2) RETURNING sales_order_item_id`, soID, itemID, int(fixtureSeq.Add(1)))
}

func (f *fixtures) stock(item int, onHand string) {
	f.insert("inventory_stock", "inventory_stock_id", `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		VALUES ($1, 1, $2::numeric) RETURNING inventory_stock_id`, item, onHand)
}

func (f *fixtures) allocate(item, so, line int, qty string) string {
	id := f.insert("inventory_allocation", "inventory_allocation_id", `
		INSERT INTO inventory_allocation (inventory_item_id, warehouse_id, sales_order_id, sales_order_item_id, allocated_quantity)
		VALUES ($1, 1, $2, $3, $4::numeric) RETURNING inventory_allocation_id`, item, so, line, qty)
	return f.uuidOf("inventory_allocation", "inventory_allocation_id", "inventory_allocation_uuid", id)
}

func (f *fixtures) itemUUID(item int) string {
	return f.uuidOf("inventory_item", "inventory_item_id", "inventory_item_uuid", item)
}

func TestScenarioPriceDriftOnConversion(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	convert := func(quoteTotal, orderTotal string) string {
		q := f.insert("quote", "quote_id", `
			INSERT INTO quote (record_type, quote_status, quote_customer_id, quote_number, quote_grand_total)
			VALUES ($1, $2, $3, $4, $5::numeric) RETURNING quote_id`,
			f.typeID("QUOT"), f.status("QUOT", "DRFT"), cust, uniq("Q"), quoteTotal)
		so, soUUID := f.salesOrder(cust, "DRFT", orderTotal)
		f.insert("quote_conversion", "quote_conversion_id", `
			INSERT INTO quote_conversion (quote_id, sales_order_id) VALUES ($1, $2) RETURNING quote_conversion_id`, q, so)
		return soUUID
	}
	drifted := convert("1000", "1300")
	same := convert("1000", "1020")
	got := f.run(RulePriceDriftOnConvert)
	flagged(t, got, drifted, "order is 30% above its quote")
	notFlagged(t, got, same, "2% difference is within tolerance")
}

func TestScenarioOverAllocated(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	so, _ := f.salesOrder(cust, "OPEN", "0")
	short := f.item("10")
	f.stock(short, "3")
	f.allocate(short, so, f.soLineID(so, short), "10")
	fine := f.item("10")
	f.stock(fine, "5")
	f.allocate(fine, so, f.soLineID(so, fine), "2")
	got := f.run(RuleOverAllocated)
	month := f.today.Format("2006-01")
	flagged(t, got, f.itemUUID(short)+":1:"+month, "10 reserved against 3 on hand")
	notFlagged(t, got, f.itemUUID(fine)+":1:"+month, "2 reserved against 5 on hand")
}

func TestScenarioReservedForClosedOrder(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	item := f.item("10")
	cancelled, _ := f.salesOrder(cust, "CANC", "0")
	stale := f.allocate(item, cancelled, f.soLineID(cancelled, item), "4")
	open, _ := f.salesOrder(cust, "OPEN", "0")
	live := f.allocate(item, open, f.soLineID(open, item), "4")
	got := f.run(RuleReservedForClosed)
	flagged(t, got, stale, "stock still reserved for a cancelled order")
	notFlagged(t, got, live, "the order is still open")
}

func TestScenarioAdjustmentBurst(t *testing.T) {
	f := newFixtures(t)
	busy, quiet := f.employee(), f.employee()
	post := func(owner int) {
		f.insert("inventory_adjustment", "inventory_adjustment_id", `
			INSERT INTO inventory_adjustment (record_type, adjustment_status, warehouse_id, adjustment_number, adjustment_posted_at,
			                                  adjustment_posted_by, adjustment_owner_id)
			VALUES ($1, $2, 1, $3, now(), 1, $4) RETURNING inventory_adjustment_id`,
			f.typeID("IADJ"), f.status("IADJ", "POST"), uniq("ADJ"), owner)
	}
	for i := 0; i < adjustmentBurstCount; i++ {
		post(busy)
	}
	for i := 0; i < adjustmentBurstCount-1; i++ {
		post(quiet)
	}
	got := f.run(RuleAdjustmentBurst)
	subject := func(emp int) string {
		var key string
		require.NoError(t, f.pool.QueryRow(context.Background(), `
			SELECT u.identity_id::text || ':' || to_char($2::date, 'IYYY-IW')
			FROM employee e JOIN users u ON u.id = e.employee_user_id WHERE e.employee_id = $1`, emp, f.today).Scan(&key))
		return key
	}
	flagged(t, got, subject(busy), "five adjustments inside a week")
	notFlagged(t, got, subject(quiet), "only four")
}

func TestScenarioCountVarianceHigh(t *testing.T) {
	f := newFixtures(t)
	item := f.item("10")
	count := func(counted string) string {
		id := f.insert("inventory_count", "inventory_count_id", `
			INSERT INTO inventory_count (record_type, count_status, warehouse_id, count_number)
			VALUES ($1, $2, 1, $3) RETURNING inventory_count_id`, f.typeID("ICNT"), f.status("ICNT", "RVW_"), uniq("IC"))
		f.insert("inventory_count_line", "inventory_count_line_id", `
			INSERT INTO inventory_count_line (inventory_count_id, line_number, inventory_item_id, system_qty, counted_qty, counted_at)
			VALUES ($1, 1, $2, 100, $3::numeric, now()) RETURNING inventory_count_line_id`, id, item, counted)
		return f.uuidOf("inventory_count", "inventory_count_id", "inventory_count_uuid", id)
	}
	off := count("80")
	near := count("99")
	got := f.run(RuleCountVarianceHigh)
	flagged(t, got, off, "20 units off a system quantity of 100")
	notFlagged(t, got, near, "1% difference")
}

func TestScenarioDeadStock(t *testing.T) {
	f := newFixtures(t)
	var unit int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT MIN(unit_id) FROM lkp_unit`).Scan(&unit))
	slab := func(item int, receivedDaysAgo int) int {
		return f.insert("inventory_slab", "inventory_slab_id", `
			INSERT INTO inventory_slab (slab_serial, slab_received_at, inventory_item_id, warehouse_id, slab_length_mm, slab_width_mm,
			                            slab_thickness_mm, slab_area, slab_area_unit_id)
			VALUES ($1, $2::date, $3, 1, 3000, 1500, 20, 4.5, $4) RETURNING inventory_slab_id`,
			uniq("S"), f.daysAgo(receivedDaysAgo), item, unit)
	}
	idle, fresh, touched := f.item("10"), f.item("10"), f.item("10")
	slab(idle, deadStockDays+30)
	slab(fresh, 5)
	moved := slab(touched, deadStockDays+30)
	f.insert("inventory_slab_ledger", "inventory_slab_ledger_id", `
		INSERT INTO inventory_slab_ledger (inventory_slab_id, inventory_item_id, warehouse_id, event, quantity_delta, occurred_at)
		VALUES ($1, $2, 1, 'adjusted', 0, now() - interval '10 days') RETURNING inventory_slab_ledger_id`, moved, touched)
	got := f.run(RuleDeadStock)
	quarter := fmt.Sprintf("%d-Q%d", f.today.Year(), (int(f.today.Month())-1)/3+1)
	flagged(t, got, f.itemUUID(idle)+":1:"+quarter, "a slab untouched for months")
	notFlagged(t, got, f.itemUUID(fresh)+":1:"+quarter, "received last week")
	notFlagged(t, got, f.itemUUID(touched)+":1:"+quarter, "moved 10 days ago")
}

func TestScenarioReceivedSlabMismatch(t *testing.T) {
	f := newFixtures(t)
	vendor := f.vendor()
	item := f.item("10")
	receive := func(expected, received int) string {
		po, _ := f.purchaseOrder(vendor, "RCVD", "10", "", nil)
		poi := f.insert("purchase_order_item", "purchase_order_item_id", `
			INSERT INTO purchase_order_item (purchase_order_id, line_number, inventory_item_id, item_name, quantity, expected_slabs)
			VALUES ($1, 1, $2, 'Granite', 10, $3) RETURNING purchase_order_item_id`, po, item, expected)
		ir := f.insert("item_receipt", "item_receipt_id", `
			INSERT INTO item_receipt (record_type, item_receipt_status, purchase_order_id, item_receipt_vendor_id, warehouse_id,
			                          item_receipt_number)
			VALUES ($1, $2, $3, $4, 1, $5) RETURNING item_receipt_id`,
			f.typeID("IRCT"), f.status("IRCT", "RCVD"), po, vendor, uniq("IR"))
		line := f.insert("item_receipt_line", "item_receipt_line_id", `
			INSERT INTO item_receipt_line (item_receipt_id, line_number, purchase_order_item_id, inventory_item_id, qty_received)
			VALUES ($1, 1, $2, $3, $4) RETURNING item_receipt_line_id`, ir, poi, item, received)
		for seq := 1; seq <= received; seq++ {
			f.insert("item_receipt_line_unit", "item_receipt_line_unit_id", `
				INSERT INTO item_receipt_line_unit (item_receipt_line_id, unit_seq, slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area)
				VALUES ($1, $2, 3000, 1500, 20, 4.5) RETURNING item_receipt_line_unit_id`, line, seq)
		}
		return f.uuidOf("purchase_order_item", "purchase_order_item_id", "purchase_order_item_uuid", poi)
	}
	short := receive(5, 3)
	exact := receive(2, 2)
	got := f.run(RuleReceivedSlabMismatch)
	flagged(t, got, short, "five slabs expected, three received")
	notFlagged(t, got, exact, "counts agree")
}

func TestScenarioShrinkageSpike(t *testing.T) {
	f := newFixtures(t)
	item := f.item("10")
	write := func(warehouse int, qty string, daysAgo int) {
		id := f.insert("inventory_adjustment", "inventory_adjustment_id", `
			INSERT INTO inventory_adjustment (record_type, adjustment_status, warehouse_id, adjustment_number, adjustment_posted_at, adjustment_posted_by)
			VALUES ($1, $2, $3, $4, now() - ($5::int || ' days')::interval, 1) RETURNING inventory_adjustment_id`,
			f.typeID("IADJ"), f.status("IADJ", "POST"), warehouse, uniq("ADJ"), daysAgo)
		f.insert("inventory_adjustment_line", "inventory_adjustment_line_id", `
			INSERT INTO inventory_adjustment_line (inventory_adjustment_id, line_number, inventory_item_id, inventory_reason_id, qty_delta)
			VALUES ($1, 1, $2, (SELECT MIN(inventory_reason_id) FROM lkp_inventory_reason), $3::numeric)
			RETURNING inventory_adjustment_line_id`, id, item, qty)
	}
	spiking := f.insert("company_location", "company_location_id",
		`INSERT INTO company_location (name) VALUES ($1) RETURNING company_location_id`, uniq("Loc"))
	steady := f.insert("company_location", "company_location_id",
		`INSERT INTO company_location (name) VALUES ($1) RETURNING company_location_id`, uniq("Loc"))
	write(spiking, "-3", shrinkagePeriodDays+10) // the period before: 3 units
	write(spiking, "-30", 5)                     // this period: 30 units
	write(steady, "-20", shrinkagePeriodDays+10)
	write(steady, "-25", 5) // up, but nowhere near double
	got := f.run(RuleShrinkageSpike)
	month := f.today.Format("2006-01")
	flagged(t, got, f.uuidOf("company_location", "company_location_id", "company_location_uuid", spiking)+":"+month, "30 units against 3 before")
	notFlagged(t, got, f.uuidOf("company_location", "company_location_id", "company_location_uuid", steady)+":"+month, "25 against 20 is not a spike")
}

func TestScenarioInstallOverdue(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	so, _ := f.salesOrder(cust, "OPEN", "10")
	job := func(promisedDaysAgo int, actual any) (string, string) {
		id := f.insert("fabrication_job", "fabrication_job_id", `
			INSERT INTO fabrication_job (record_type, fabrication_job_status, sales_order_id, fabrication_job_customer_id, fabrication_job_number,
			                             job_promised_install_date, job_actual_install_date)
			VALUES ($1, $2, $3, $4, $5, $6::date, $7::date) RETURNING fabrication_job_id`,
			f.typeID("FJOB"), f.status("FJOB", "ORCV"), so, cust, uniq("FJ"), f.daysAgo(promisedDaysAgo), actual)
		return f.uuidOf("fabrication_job", "fabrication_job_id", "fabrication_job_uuid", id), f.daysAgo(promisedDaysAgo)
	}
	late, lateDate := job(10, nil)
	upcoming, upcomingDate := job(-10, nil)
	done, doneDate := job(10, f.daysAgo(8))
	got := f.run(RuleInstallOverdue)
	flagged(t, got, late+":"+lateDate, "promised 10 days ago, no install yet")
	notFlagged(t, got, upcoming+":"+upcomingDate, "promised for the future")
	notFlagged(t, got, done+":"+doneDate, "already installed")
}

func TestScenarioManyTemplateRevisions(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	so, _ := f.salesOrder(cust, "OPEN", "10")
	job := func(revisions int) string {
		id := f.insert("fabrication_job", "fabrication_job_id", `
			INSERT INTO fabrication_job (record_type, fabrication_job_status, sales_order_id, fabrication_job_customer_id, fabrication_job_number)
			VALUES ($1, $2, $3, $4, $5) RETURNING fabrication_job_id`,
			f.typeID("FJOB"), f.status("FJOB", "ORCV"), so, cust, uniq("FJ"))
		for r := 1; r <= revisions; r++ {
			state := "superseded" // only the latest revision may be current
			if r == revisions {
				state = "submitted"
			}
			f.insert("fabrication_template_revision", "template_id", `
				INSERT INTO fabrication_template_revision (fabrication_job_id, revision, sales_order_version, baseline, measured_lines, change_summary, state)
				VALUES ($1, $2, 1, '[]', '[]', '{}', $3) RETURNING template_id`, id, r, state)
		}
		return f.uuidOf("fabrication_job", "fabrication_job_id", "fabrication_job_uuid", id)
	}
	redone := job(templateRevisionCount)
	normal := job(templateRevisionCount - 1)
	got := f.run(RuleManyTemplateRevs)
	flagged(t, got, redone, "four revisions")
	notFlagged(t, got, normal, "three revisions")
}

func TestScenarioMultipleJobsPerOrder(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	add := func(so int) {
		f.insert("fabrication_job", "fabrication_job_id", `
			INSERT INTO fabrication_job (record_type, fabrication_job_status, sales_order_id, fabrication_job_customer_id, fabrication_job_number)
			VALUES ($1, $2, $3, $4, $5) RETURNING fabrication_job_id`,
			f.typeID("FJOB"), f.status("FJOB", "ORCV"), so, cust, uniq("FJ"))
	}
	two, twoUUID := f.salesOrder(cust, "OPEN", "10")
	add(two)
	add(two)
	one, oneUUID := f.salesOrder(cust, "OPEN", "10")
	add(one)
	got := f.run(RuleMultipleJobsPerSale)
	flagged(t, got, twoUUID+":2", "two jobs on one order")
	notFlagged(t, got, oneUUID+":1", "a single job")
}

func TestScenarioUnusualCashTransfer(t *testing.T) {
	f := newFixtures(t)
	var from, to int
	require.NoError(t, f.pool.QueryRow(context.Background(),
		`SELECT MIN(coa_account_id), MAX(coa_account_id) FROM coa_account`).Scan(&from, &to))
	transfer := func(amount string) string {
		id := f.insert("cash_transfer", "cash_transfer_id", `
			INSERT INTO cash_transfer (record_type, cash_transfer_status, from_account_id, to_account_id, cash_transfer_amount, cash_transfer_number)
			VALUES ($1, $2, $3, $4, $5::numeric, $6) RETURNING cash_transfer_id`,
			f.typeID("CTRF"), f.status("CTRF", "APPR"), from, to, amount, uniq("CT"))
		return f.uuidOf("cash_transfer", "cash_transfer_id", "cash_transfer_uuid", id)
	}
	for i := 0; i < cashTransferHistory; i++ {
		transfer("100")
	}
	big := transfer("2000")
	small := transfer("150")
	got := f.run(RuleUnusualCashTransfer)
	flagged(t, got, big, "2000 against a usual 100")
	notFlagged(t, got, small, "150 is ordinary")
}

func TestScenarioChartOfAccountsChanged(t *testing.T) {
	f := newFixtures(t)
	var account int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT MIN(coa_account_id) FROM coa_account`).Scan(&account))
	change := func(daysAgo int) string {
		id := f.insert("coa_account_history", "coa_account_history_id", `
			INSERT INTO coa_account_history (coa_account_id, history_action, history_field, history_at)
			VALUES ($1, 'update', 'coa_account_name', now() - ($2::int || ' days')::interval) RETURNING coa_account_history_id`, account, daysAgo)
		return fmt.Sprintf("coa:%d", id)
	}
	recent := change(1)
	old := change(coaChangeDays + 20)
	got := f.run(RuleChartOfAccountsEdit)
	flagged(t, got, recent, "edited yesterday")
	notFlagged(t, got, old, "edited weeks ago")
}

func TestScenarioPartialPOStalled(t *testing.T) {
	f := newFixtures(t)
	vendor := f.vendor()
	partial := func(lastReceiptDaysAgo int) string {
		po, poUUID := f.purchaseOrder(vendor, "PART", "10", "", nil)
		f.insert("item_receipt", "item_receipt_id", `
			INSERT INTO item_receipt (record_type, item_receipt_status, purchase_order_id, item_receipt_vendor_id, warehouse_id,
			                          item_receipt_number, item_receipt_date)
			VALUES ($1, $2, $3, $4, 1, $5, $6::date) RETURNING item_receipt_id`,
			f.typeID("IRCT"), f.status("IRCT", "RCVD"), po, vendor, uniq("IR"), f.daysAgo(lastReceiptDaysAgo))
		return poUUID
	}
	stalled := partial(poPartialStuckDays + 20)
	active := partial(2)
	got := f.run(RulePOPartialStuck)
	flagged(t, got, stalled, "last receipt 50 days ago")
	notFlagged(t, got, active, "received two days ago")
}

func TestScenarioExpenseRules(t *testing.T) {
	f := newFixtures(t)
	var category int
	require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT MIN(expense_category_id) FROM lkp_expense_category`).Scan(&category))
	claimant := f.employee()
	other := f.employee()
	expense := func(who int, amounts ...string) (int, string) {
		id := f.insert("expense", "expense_id", `
			INSERT INTO expense (record_type, expense_status, expense_claimant_id, expense_number)
			VALUES ($1, $2, $3, $4) RETURNING expense_id`, f.typeID("EXPN"), f.status("EXPN", "APPV"), who, uniq("EX"))
		for i, amt := range amounts {
			f.insert("expense_item", "expense_item_id", `
				INSERT INTO expense_item (expense_id, line_number, category_id, expense_date, amount)
				VALUES ($1, $2, $3, $4::date, $5::numeric) RETURNING expense_item_id`, id, i+1, category, f.daysAgo(2), amt)
		}
		return id, f.uuidOf("expense", "expense_id", "expense_uuid", id)
	}
	itemUUID := func(expenseID int, line int) string {
		var u string
		require.NoError(t, f.pool.QueryRow(context.Background(),
			`SELECT expense_item_uuid::text FROM expense_item WHERE expense_id = $1 AND line_number = $2`, expenseID, line).Scan(&u))
		return u
	}

	// duplicate: the same person, amount, date and category twice
	_, first := expense(claimant, "77.00")
	_, second := expense(claimant, "77.00")
	_, differentPerson := expense(other, "77.00")
	dups := f.run(RuleExpenseDuplicate)
	flagged(t, dups, second, "same person, amount, date and category as an earlier expense")
	notFlagged(t, dups, first, "the first claim is the original")
	notFlagged(t, dups, differentPerson, "a different person")

	// unusually high: ten ordinary lines, then one far above
	history := make([]string, expenseMinHistory)
	for i := range history {
		history[i] = fmt.Sprintf("%d.00", 50+i) // distinct amounts so none count as duplicates
	}
	expense(other, history...)
	bigID, _ := expense(claimant, "900.00")
	smallID, _ := expense(claimant, "58.50")
	high := f.run(RuleExpenseUnusuallyHigh)
	flagged(t, high, itemUUID(bigID, 1), "900 against a median near 55")
	notFlagged(t, high, itemUUID(smallID, 1), "an ordinary amount")
}

func TestScenarioPOPriceAboveCost(t *testing.T) {
	f := newFixtures(t)
	vendor := f.vendor()
	item := f.item("100")
	build := func(price string) string {
		po, poUUID := f.purchaseOrder(vendor, "DRFT", "10", "", nil)
		f.insert("purchase_order_item", "purchase_order_item_id", `
			INSERT INTO purchase_order_item (purchase_order_id, line_number, inventory_item_id, item_name, quantity, unit_price)
			VALUES ($1, 1, $2, 'Granite', 1, $3::numeric) RETURNING purchase_order_item_id`, po, item, price)
		return poUUID
	}
	dear := build("400")
	fair := build("110")
	got := f.run(RulePOPriceAboveCost)
	flagged(t, got, dear, "400 against a usual cost of 100")
	notFlagged(t, got, fair, "10% above the usual cost")
}
