//go:build dbtest

package anomaly

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/dbtestutil"
)

// Each scenario seeds one record that should trip a rule and one similar control
// that should not, runs only that rule, and checks the subjects it returns. The
// fixtures are removed afterwards (newest first, so children go before parents).

var fixtureSeq atomic.Int64

// uniq returns a short unique string: record numbers in these tables are narrow columns.
func uniq(prefix string) string {
	return fmt.Sprintf("%s%x%d", prefix, time.Now().UnixNano()/1000%0xfffffff, fixtureSeq.Add(1))
}

type fixtures struct {
	t     *testing.T
	pool  *pgxpool.Pool
	undo  []string
	today time.Time
}

func newFixtures(t *testing.T) *fixtures {
	t.Helper()
	pool := newTestPool(t)
	dbtestutil.EnsureLocation(t, pool)
	f := &fixtures{t: t, pool: pool, today: time.Now()}
	t.Cleanup(func() {
		for i := len(f.undo) - 1; i >= 0; i-- {
			_, _ = pool.Exec(context.Background(), f.undo[i])
		}
	})
	return f
}

// insert runs an INSERT ... RETURNING id (and optionally a second value) and
// remembers how to delete the row again.
func (f *fixtures) insert(table, idCol, sql string, args ...any) int {
	f.t.Helper()
	var id int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), sql, args...).Scan(&id), "insert into %s", table)
	f.undo = append(f.undo, fmt.Sprintf("DELETE FROM %s WHERE %s = %d", table, idCol, id))
	return id
}

func (f *fixtures) uuidOf(table, idCol, uuidCol string, id int) string {
	f.t.Helper()
	var u string
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		fmt.Sprintf("SELECT %s::text FROM %s WHERE %s = $1", uuidCol, table, idCol), id).Scan(&u))
	return u
}

func (f *fixtures) typeID(code string) int {
	f.t.Helper()
	var id int
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id))
	return id
}

func (f *fixtures) status(typeCode, statusCode string) int {
	f.t.Helper()
	var id int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `
		SELECT rs.record_status_id FROM lkp_record_status rs
		JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
		WHERE rt.record_type_code = $1 AND rs.record_status_code = $2`, typeCode, statusCode).Scan(&id), "%s/%s", typeCode, statusCode)
	return id
}

func (f *fixtures) daysAgo(n int) string { return f.today.AddDate(0, 0, -n).Format("2006-01-02") }

func (f *fixtures) customer(limit string, taxExempt bool) (int, string) {
	id := f.insert("customer", "customer_id", `
		INSERT INTO customer (record_type, customer_name, customer_credit_limit, customer_is_tax_exempt)
		VALUES ($1, $2, $3::numeric, $4) RETURNING customer_id`, f.typeID("CUST"), uniq("Cust"), limit, taxExempt)
	return id, f.uuidOf("customer", "customer_id", "customer_uuid", id)
}

type invoiceSpec struct {
	status, grand, balance, tax, subtotal string
	date, due                             string
}

func (f *fixtures) invoice(cust int, s invoiceSpec) (int, string) {
	if s.status == "" {
		s.status = "DRFT"
	}
	if s.date == "" {
		s.date = f.daysAgo(0)
	}
	if s.due == "" {
		s.due = f.daysAgo(0)
	}
	id := f.insert("invoice", "invoice_id", `
		INSERT INTO invoice (record_type, invoice_status, invoice_customer_id, invoice_number, invoice_date, invoice_due_date,
		                     invoice_grand_total, invoice_balance_due, invoice_tax_total, invoice_subtotal)
		VALUES ($1, $2, $3, $4, $5::date, $6::date, $7::numeric, $8::numeric, $9::numeric, $10::numeric) RETURNING invoice_id`,
		f.typeID("INVC"), f.status("INVC", s.status), cust, uniq("INV"), s.date, s.due,
		nz(s.grand), nz(s.balance), nz(s.tax), nz(s.subtotal))
	return id, f.uuidOf("invoice", "invoice_id", "invoice_uuid", id)
}

func nz(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func (f *fixtures) salesOrder(cust int, status, grand string) (int, string) {
	id := f.insert("sales_order", "sales_order_id", `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id, sales_order_number, sales_order_grand_total)
		VALUES ($1, $2, $3, $4, $5::numeric) RETURNING sales_order_id`,
		f.typeID("SORD"), f.status("SORD", status), cust, uniq("SO"), nz(grand))
	return id, f.uuidOf("sales_order", "sales_order_id", "sales_order_uuid", id)
}

func (f *fixtures) soLine(soID, line int, discount, price string, itemID any) {
	f.insert("sales_order_item", "sales_order_item_id", `
		INSERT INTO sales_order_item (sales_order_id, line_number, discount_percent, unit_price, inventory_item_id)
		VALUES ($1, $2, $3::numeric, $4::numeric, $5) RETURNING sales_order_item_id`, soID, line, discount, price, itemID)
}

func (f *fixtures) item(purchasePrice string) int {
	return f.insert("inventory_item", "inventory_item_id", `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_purchase_unit_price)
		VALUES ($1, $2, (SELECT MIN(unit_id) FROM lkp_unit), $3::numeric) RETURNING inventory_item_id`, uniq("SKU"), uniq("Item"), purchasePrice)
}

func (f *fixtures) payMethod() int {
	f.t.Helper()
	var id int
	require.NoError(f.t, f.pool.QueryRow(context.Background(), `SELECT MIN(payment_method_id) FROM lkp_payment_method`).Scan(&id))
	return id
}

func (f *fixtures) payment(cust int, status, amount, unapplied, date string) (int, string) {
	id := f.insert("payment", "payment_id", `
		INSERT INTO payment (record_type, payment_status, payment_customer_id, payment_method, payment_amount,
		                     payment_unapplied_amount, payment_date, payment_number)
		VALUES ($1, $2, $3, $4, $5::numeric, $6::numeric, $7::date, $8) RETURNING payment_id`,
		f.typeID("PYMT"), f.status("PYMT", status), cust, f.payMethod(), amount, unapplied, date, uniq("PAY"))
	return id, f.uuidOf("payment", "payment_id", "payment_uuid", id)
}

func (f *fixtures) vendor() int {
	return f.insert("vendor", "vendor_id", `
		INSERT INTO vendor (record_type, vendor_status, vendor_legal_name)
		VALUES ($1, $2, $3) RETURNING vendor_id`, f.typeID("VNDR"), f.status("VNDR", "ACT_"), uniq("Vendor"))
}

func (f *fixtures) purchaseOrder(vendor int, status, grand, expected string, owner any) (int, string) {
	id := f.insert("purchase_order", "purchase_order_id", `
		INSERT INTO purchase_order (record_type, purchase_order_status, purchase_order_vendor_id, purchase_order_number,
		                            purchase_order_grand_total, purchase_order_expected_date, purchase_order_owner_id)
		VALUES ($1, $2, $3, $4, $5::numeric, $6::date, $7) RETURNING purchase_order_id`,
		f.typeID("PORD"), f.status("PORD", status), vendor, uniq("PO"), nz(grand), nullable(expected), owner)
	return id, f.uuidOf("purchase_order", "purchase_order_id", "purchase_order_uuid", id)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// run executes one rule by key and returns its findings by subject.
func (f *fixtures) run(key string) map[string]Finding {
	f.t.Helper()
	for _, r := range DefaultRules() {
		if r.Key() != key {
			continue
		}
		found, err := r.Detect(context.Background(), f.pool, f.today)
		require.NoError(f.t, err, key)
		out := map[string]Finding{}
		for _, fd := range found {
			out[fd.SubjectKey] = fd
		}
		return out
	}
	f.t.Fatalf("no rule %q", key)
	return nil
}

func flagged(t *testing.T, got map[string]Finding, subject, why string) Finding {
	t.Helper()
	fd, ok := got[subject]
	require.True(t, ok, "should be flagged: %s", why)
	return fd
}

func notFlagged(t *testing.T, got map[string]Finding, subject, why string) {
	t.Helper()
	_, ok := got[subject]
	assert.False(t, ok, "should NOT be flagged: %s", why)
}

func TestScenarioHighDiscount(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	normal, normalUUID := f.salesOrder(cust, "DRFT", "100")
	for line := 1; line <= 10; line++ {
		f.soLine(normal, line, "5", "10", nil)
	}
	odd, oddUUID := f.salesOrder(cust, "DRFT", "100")
	f.soLine(odd, 1, "60", "10", nil)
	got := f.run(RuleHighDiscount)
	flagged(t, got, oddUUID, "a 60% line against a workspace norm of about 10%")
	notFlagged(t, got, normalUUID, "5% lines are normal")
}

func TestScenarioBelowCostSale(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	item := f.item("100")
	low, lowUUID := f.salesOrder(cust, "DRFT", "0")
	f.soLine(low, 1, "0", "60", item)
	ok, okUUID := f.salesOrder(cust, "DRFT", "0")
	f.soLine(ok, 1, "0", "120", item)
	got := f.run(RuleBelowCostSale)
	flagged(t, got, lowUUID, "priced at 60 against a cost of 100")
	notFlagged(t, got, okUUID, "priced above cost")
}

func TestScenarioUnusualOrderTotal(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	for i := 0; i < minHistory; i++ {
		f.salesOrder(cust, "OPEN", "100")
	}
	_, bigUUID := f.salesOrder(cust, "OPEN", "5000")
	_, smallUUID := f.salesOrder(cust, "OPEN", "120")
	got := f.run(RuleUnusualOrderTotal)
	fd := flagged(t, got, bigUUID, "5000 against a median of 100")
	assert.Contains(t, fd.Detail, "5000")
	notFlagged(t, got, smallUUID, "close to the customer's usual size")
}

func TestScenarioDuplicateInvoice(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	_, firstUUID := f.invoice(cust, invoiceSpec{grand: "500"})
	_, dupUUID := f.invoice(cust, invoiceSpec{grand: "500"})
	_, otherUUID := f.invoice(cust, invoiceSpec{grand: "501"})
	got := f.run(RuleDuplicateInvoice)
	flagged(t, got, dupUUID, "same customer, total and date as an earlier invoice")
	notFlagged(t, got, firstUUID, "the first invoice is the original")
	notFlagged(t, got, otherUUID, "a different total")
}

func TestScenarioOverdueInvoice(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	_, lateUUID := f.invoice(cust, invoiceSpec{status: "SENT", balance: "50", due: f.daysAgo(60)})
	_, recentUUID := f.invoice(cust, invoiceSpec{status: "SENT", balance: "50", due: f.daysAgo(5)})
	_, paidUUID := f.invoice(cust, invoiceSpec{status: "PAID", balance: "0", due: f.daysAgo(60)})
	_, ancientUUID := f.invoice(cust, invoiceSpec{status: "SENT", balance: "50", due: f.daysAgo(400)})
	got := f.run(RuleOverdueInvoice)
	fd := flagged(t, got, lateUUID, "60 days past due with a balance")
	assert.Contains(t, fd.Detail, "60 days")
	notFlagged(t, got, recentUUID, "only 5 days late")
	notFlagged(t, got, paidUUID, "paid")
	notFlagged(t, got, ancientUUID, "too old to alert on")
}

func TestScenarioOverCreditLimit(t *testing.T) {
	f := newFixtures(t)
	over, overUUID := f.customer("100", false)
	f.invoice(over, invoiceSpec{status: "SENT", balance: "500"})
	under, underUUID := f.customer("1000", false)
	f.invoice(under, invoiceSpec{status: "SENT", balance: "500"})
	none, noneUUID := f.customer("0", false)
	f.invoice(none, invoiceSpec{status: "SENT", balance: "500"})
	got := f.run(RuleOverCreditLimit)
	month := f.today.Format("2006-01")
	flagged(t, got, overUUID+":"+month, "500 owing against a limit of 100")
	notFlagged(t, got, underUUID+":"+month, "within the limit")
	notFlagged(t, got, noneUUID+":"+month, "no limit set")
}

func TestScenarioCreditMemoBurst(t *testing.T) {
	f := newFixtures(t)
	busy, busyUUID := f.customer("0", false)
	quiet, quietUUID := f.customer("0", false)
	add := func(cust int) {
		f.insert("credit_memo", "credit_memo_id", `
			INSERT INTO credit_memo (record_type, credit_memo_status, credit_memo_customer_id, credit_memo_number)
			VALUES ($1, $2, $3, $4) RETURNING credit_memo_id`, f.typeID("CRDT"), f.status("CRDT", "DRFT"), cust, uniq("CM"))
	}
	for i := 0; i < creditMemoBurstCount; i++ {
		add(busy)
	}
	for i := 0; i < creditMemoBurstCount-1; i++ {
		add(quiet)
	}
	got := f.run(RuleCreditMemoBurst)
	month := f.today.Format("2006-01")
	flagged(t, got, busyUUID+":"+month, "three credit memos in a month")
	notFlagged(t, got, quietUUID+":"+month, "only two")
}

func TestScenarioRefundRules(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	addRefund := func(paymentID int, amount, date string) string {
		id := f.insert("refund", "refund_id", `
			INSERT INTO refund (record_type, refund_status, refund_customer_id, refund_method, refund_amount, refund_payment_id,
			                    refund_date, refund_number)
			VALUES ($1, $2, $3, $4, $5::numeric, $6, $7::date, $8) RETURNING refund_id`,
			f.typeID("RFND"), f.status("RFND", "PEND"), cust, f.payMethod(), amount, paymentID, date, uniq("RF"))
		return f.uuidOf("refund", "refund_id", "refund_uuid", id)
	}
	pay, _ := f.payment(cust, "APPV", "100", "0", f.daysAgo(20))
	tooMuch := addRefund(pay, "150", f.daysAgo(10))
	fresh, _ := f.payment(cust, "APPV", "1000", "0", f.daysAgo(0))
	quick := addRefund(fresh, "600", f.daysAgo(0))
	sane := addRefund(pay, "40", f.daysAgo(5))

	exceeds := f.run(RuleRefundExceedsPayment)
	flagged(t, exceeds, tooMuch, "refunds 150 of a 100 payment")
	notFlagged(t, exceeds, sane, "a normal partial refund")

	quickly := f.run(RuleQuickRefund)
	flagged(t, quickly, quick, "600 refunded the same day as the payment")
	notFlagged(t, quickly, sane, "refunded 15 days after the payment")
}

func TestScenarioStaleUnappliedPayment(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	_, staleUUID := f.payment(cust, "APPV", "80", "80", f.daysAgo(60))
	_, appliedUUID := f.payment(cust, "APPV", "80", "0", f.daysAgo(60))
	_, freshUUID := f.payment(cust, "APPV", "80", "80", f.daysAgo(3))
	got := f.run(RuleStaleUnappliedPayment)
	flagged(t, got, staleUUID, "80 unapplied for 60 days")
	notFlagged(t, got, appliedUUID, "fully applied")
	notFlagged(t, got, freshUUID, "only 3 days old")
}

func TestScenarioVoidedAfterPosting(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	voidID := f.status("INVC", "VOID")
	issued, issuedUUID := f.invoice(cust, invoiceSpec{status: "VOID"})
	draft, draftUUID := f.invoice(cust, invoiceSpec{status: "VOID"})
	hist := func(inv int, from string) int {
		return f.insert("invoice_history", "invoice_history_id", `
			INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action)
			VALUES ($1, $2, $3, 'transition') RETURNING invoice_history_id`, inv, f.status("INVC", from), voidID)
	}
	issuedHist := hist(issued, "SENT")
	draftHist := hist(draft, "DRFT")
	got := f.run(RuleVoidedAfterPosting)
	fd := flagged(t, got, fmt.Sprintf("invoice-void:%d", issuedHist), "voided after being sent")
	assert.Equal(t, issuedUUID, fd.RecordUUID)
	notFlagged(t, got, fmt.Sprintf("invoice-void:%d", draftHist), "a draft voided before it ever went out")
	_ = draftUUID
}

func TestScenarioBackdatedDocument(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	_, oldUUID := f.invoice(cust, invoiceSpec{date: f.daysAgo(90)})
	_, todayUUID := f.invoice(cust, invoiceSpec{date: f.daysAgo(0)})
	got := f.run(RuleBackdatedDocument)
	flagged(t, got, "invoice:"+oldUUID, "dated 90 days before it was entered")
	notFlagged(t, got, "invoice:"+todayUUID, "dated today")
}

func TestScenarioZeroTaxUnusual(t *testing.T) {
	f := newFixtures(t)
	taxed, _ := f.customer("0", false)
	for i := 0; i < zeroTaxMinHistory; i++ {
		f.invoice(taxed, invoiceSpec{tax: "10", subtotal: "100", grand: "110"})
	}
	_, noTaxUUID := f.invoice(taxed, invoiceSpec{tax: "0", subtotal: "100", grand: "100"})
	exempt, _ := f.customer("0", true)
	for i := 0; i < zeroTaxMinHistory; i++ {
		f.invoice(exempt, invoiceSpec{tax: "10", subtotal: "100", grand: "110"})
	}
	_, exemptUUID := f.invoice(exempt, invoiceSpec{tax: "0", subtotal: "100", grand: "100"})
	got := f.run(RuleZeroTaxUnusual)
	flagged(t, got, noTaxUUID, "no tax for a customer whose last invoices were taxed")
	notFlagged(t, got, exemptUUID, "the customer is tax exempt")
}

func TestScenarioFilledNotInvoiced(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	fill := f.status("SORD", "FILL")
	filledAt := f.today.AddDate(0, 0, -30)
	makeFilled := func() (int, string) {
		id, u := f.salesOrder(cust, "FILL", "100")
		f.insert("sales_order_history", "sales_order_history_id", `
			INSERT INTO sales_order_history (sales_order_id, from_status_id, to_status_id, action, at)
			VALUES ($1, $2, $2, 'transition', $3) RETURNING sales_order_history_id`, id, fill, filledAt)
		return id, u
	}
	_, lonelyUUID := makeFilled()
	billedID, billedUUID := makeFilled()
	invID, _ := f.invoice(cust, invoiceSpec{status: "SENT", grand: "100"})
	_, err := f.pool.Exec(context.Background(), `UPDATE invoice SET invoice_sales_order_id = $1 WHERE invoice_id = $2`, billedID, invID)
	require.NoError(t, err)
	got := f.run(RuleFilledNotInvoiced)
	flagged(t, got, lonelyUUID, "filled 30 days ago with no invoice")
	notFlagged(t, got, billedUUID, "has an invoice")
}

func TestScenarioPurchasingRules(t *testing.T) {
	f := newFixtures(t)
	vendor := f.vendor()

	// bill_exceeds_po
	_, overUUID := f.purchaseOrder(vendor, "SENT", "100", f.daysAgo(-5), nil)
	overID := f.idOf("purchase_order", "purchase_order_uuid", overUUID)
	_, fineUUID := f.purchaseOrder(vendor, "SENT", "100", f.daysAgo(-5), nil)
	fineID := f.idOf("purchase_order", "purchase_order_uuid", fineUUID)
	bill := func(po int, total string) {
		f.insert("vendor_bill", "vendor_bill_id", `
			INSERT INTO vendor_bill (record_type, vendor_bill_status, vendor_bill_vendor_id, vendor_bill_purchase_order_id,
			                         vendor_bill_grand_total, vendor_bill_number)
			VALUES ($1, $2, $3, $4, $5::numeric, $6) RETURNING vendor_bill_id`,
			f.typeID("VBIL"), f.status("VBIL", "DRFT"), vendor, po, total, uniq("VB"))
	}
	bill(overID, "150")
	bill(fineID, "90")
	got := f.run(RuleBillExceedsPO)
	flagged(t, got, overUUID, "bills of 150 against a 100 order")
	notFlagged(t, got, fineUUID, "billed 90 of 100")

	// po_late
	_, lateUUID := f.purchaseOrder(vendor, "SENT", "10", f.daysAgo(10), nil)
	_, futureUUID := f.purchaseOrder(vendor, "SENT", "10", f.daysAgo(-5), nil)
	_, receivedUUID := f.purchaseOrder(vendor, "RCVD", "10", f.daysAgo(10), nil)
	late := f.run(RulePOLate)
	flagged(t, late, lateUUID, "expected 10 days ago and still open")
	notFlagged(t, late, futureUUID, "not due yet")
	notFlagged(t, late, receivedUUID, "already received")

	// split_purchase_orders (employee 1 is seeded)
	splitVendor := f.vendor()
	for i := 0; i < splitPOCount; i++ {
		f.purchaseOrder(splitVendor, "APPV", "10", "", 1)
	}
	splitSubject := fmt.Sprintf("%s:1:%s", f.uuidOf("vendor", "vendor_id", "vendor_uuid", splitVendor), f.daysAgo(0))
	split := f.run(RuleSplitPurchaseOrders)
	flagged(t, split, splitSubject, "three POs to one vendor by one person today")
}

func (f *fixtures) idOf(table, uuidCol, uuid string) int {
	f.t.Helper()
	idCol := map[string]string{"purchase_order": "purchase_order_id"}[table]
	var id int
	require.NoError(f.t, f.pool.QueryRow(context.Background(),
		fmt.Sprintf("SELECT %s FROM %s WHERE %s = $1::uuid", idCol, table, uuidCol), uuid).Scan(&id))
	return id
}

func TestScenarioVendorOverpaid(t *testing.T) {
	f := newFixtures(t)
	vendor := f.vendor()
	bill := func(total string) (int, string) {
		id := f.insert("vendor_bill", "vendor_bill_id", `
			INSERT INTO vendor_bill (record_type, vendor_bill_status, vendor_bill_vendor_id, vendor_bill_grand_total, vendor_bill_number)
			VALUES ($1, $2, $3, $4::numeric, $5) RETURNING vendor_bill_id`,
			f.typeID("VBIL"), f.status("VBIL", "APPV"), vendor, total, uniq("VB"))
		return id, f.uuidOf("vendor_bill", "vendor_bill_id", "vendor_bill_uuid", id)
	}
	pay := func() int {
		return f.insert("vendor_payment", "vendor_payment_id", `
			INSERT INTO vendor_payment (record_type, vendor_payment_status, vendor_payment_vendor_id, vendor_payment_method,
			                            vendor_payment_amount, vendor_payment_number)
			VALUES ($1, $2, $3, $4, 100, $5) RETURNING vendor_payment_id`,
			f.typeID("VPAY"), f.status("VPAY", "DRFT"), vendor, f.payMethod(), uniq("VP"))
	}
	apply := func(paymentID, billID int, amount string) {
		f.insert("vendor_payment_application", "application_id", `
			INSERT INTO vendor_payment_application (vendor_payment_id, vendor_bill_id, application_amount)
			VALUES ($1, $2, $3::numeric) RETURNING application_id`, paymentID, billID, amount)
	}
	overID, overUUID := bill("100")
	apply(pay(), overID, "100")
	apply(pay(), overID, "50") // 150 applied to a bill of 100
	fineID, fineUUID := bill("100")
	apply(pay(), fineID, "100")
	got := f.run(RuleVendorOverpaid)
	flagged(t, got, overUUID, "150 applied to a 100 bill")
	notFlagged(t, got, fineUUID, "applied exactly")
}

func TestScenarioNewVendorLargePayment(t *testing.T) {
	f := newFixtures(t)
	vendor := f.vendor() // created just now
	pay := func(amount string) string {
		id := f.insert("vendor_payment", "vendor_payment_id", `
			INSERT INTO vendor_payment (record_type, vendor_payment_status, vendor_payment_vendor_id, vendor_payment_method,
			                            vendor_payment_amount, vendor_payment_number)
			VALUES ($1, $2, $3, $4, $5::numeric, $6) RETURNING vendor_payment_id`,
			f.typeID("VPAY"), f.status("VPAY", "APPV"), vendor, f.payMethod(), amount, uniq("VP"))
		return f.uuidOf("vendor_payment", "vendor_payment_id", "vendor_payment_uuid", id)
	}
	big := pay("9000")
	small := pay("100")
	got := f.run(RuleNewVendorLargePayment)
	flagged(t, got, big, "9000 to a vendor created today")
	notFlagged(t, got, small, "a small payment")
}

func TestScenarioBinTransferStuck(t *testing.T) {
	f := newFixtures(t)
	add := func(status string, ageDays int) string {
		id := f.insert("bin_transfer", "bin_transfer_id", `
			INSERT INTO bin_transfer (record_type, bin_transfer_status, warehouse_id, bin_transfer_number, bin_transfer_created_at)
			VALUES ($1, $2, 1, $3, $4::date) RETURNING bin_transfer_id`,
			f.typeID("BTRF"), f.status("BTRF", status), uniq("BT"), f.daysAgo(ageDays))
		return f.uuidOf("bin_transfer", "bin_transfer_id", "bin_transfer_uuid", id)
	}
	stuck := add("PAPV", 20)
	fresh := add("PAPV", 2)
	done := add("COMP", 20)
	got := f.run(RuleBinTransferStuck)
	flagged(t, got, stuck, "waiting approval for 20 days")
	notFlagged(t, got, fresh, "only 2 days")
	notFlagged(t, got, done, "completed")
}

func TestScenarioLargeWriteOff(t *testing.T) {
	f := newFixtures(t)
	item := f.item("20")
	add := func(qty string) string {
		id := f.insert("inventory_adjustment", "inventory_adjustment_id", `
			INSERT INTO inventory_adjustment (record_type, adjustment_status, warehouse_id, adjustment_number, adjustment_posted_at, adjustment_posted_by)
			VALUES ($1, $2, 1, $3, now(), 1) RETURNING inventory_adjustment_id`,
			f.typeID("IADJ"), f.status("IADJ", "POST"), uniq("ADJ"))
		f.insert("inventory_adjustment_line", "inventory_adjustment_line_id", `
			INSERT INTO inventory_adjustment_line (inventory_adjustment_id, line_number, inventory_item_id, inventory_reason_id, qty_delta)
			VALUES ($1, 1, $2, (SELECT MIN(inventory_reason_id) FROM lkp_inventory_reason), $3::numeric)
			RETURNING inventory_adjustment_line_id`, id, item, qty)
		return f.uuidOf("inventory_adjustment", "inventory_adjustment_id", "inventory_adjustment_uuid", id)
	}
	big := add("-100") // 100 x 20 = 2000
	small := add("-5") // 100
	got := f.run(RuleLargeWriteOff)
	flagged(t, got, big, "wrote off about 2000 of stock")
	notFlagged(t, got, small, "wrote off about 100")
}

func TestScenarioFabricationJobStuck(t *testing.T) {
	f := newFixtures(t)
	cust, _ := f.customer("0", false)
	so, _ := f.salesOrder(cust, "OPEN", "10")
	add := func(status string, ageDays int) string {
		id := f.insert("fabrication_job", "fabrication_job_id", `
			INSERT INTO fabrication_job (record_type, fabrication_job_status, sales_order_id, fabrication_job_customer_id,
			                             fabrication_job_number, fabrication_job_created_at)
			VALUES ($1, $2, $3, $4, $5, $6::date) RETURNING fabrication_job_id`,
			f.typeID("FJOB"), f.status("FJOB", status), so, cust, uniq("FJ"), f.daysAgo(ageDays))
		return f.uuidOf("fabrication_job", "fabrication_job_id", "fabrication_job_uuid", id)
	}
	stuck := add("ORCV", 40)
	fresh := add("ORCV", 3)
	done := add("COMP", 40)
	got := f.run(RuleJobStuck)
	flagged(t, got, stuck+":ORCV", "in Order Received for 40 days")
	notFlagged(t, got, fresh+":ORCV", "only 3 days")
	notFlagged(t, got, done+":COMP", "completed jobs are expected to sit still")
}

func TestScenarioPostedInClosedPeriod(t *testing.T) {
	f := newFixtures(t)
	year := f.insert("fiscal_year", "fiscal_year_id", `
		INSERT INTO fiscal_year (fiscal_year_name, fiscal_year_start, fiscal_year_end)
		VALUES ($1, '2001-01-01', '2001-12-31') RETURNING fiscal_year_id`, uniq("FY"))
	f.insert("accounting_period", "accounting_period_id", `
		INSERT INTO accounting_period (fiscal_year_id, accounting_period_name, period_number, period_start, period_end,
		                               accounting_period_status, accounting_period_closed_at)
		VALUES ($1, 'Jan 2001', 1, '2001-01-01', '2001-01-31', 'closed', now() - interval '1 day') RETURNING accounting_period_id`, year)
	inside := f.insert("journal_entry", "journal_entry_id", `
		INSERT INTO journal_entry (entry_date, source_type, source_id)
		VALUES ('2001-01-15', 'dbtest', gen_random_uuid()) RETURNING journal_entry_id`)
	outside := f.insert("journal_entry", "journal_entry_id", `
		INSERT INTO journal_entry (entry_date, source_type, source_id)
		VALUES ('2001-03-15', 'dbtest', gen_random_uuid()) RETURNING journal_entry_id`)
	got := f.run(RulePostedInClosedPeriod)
	flagged(t, got, f.uuidOf("journal_entry", "journal_entry_id", "journal_entry_uuid", inside), "dated in a closed month, posted after it closed")
	notFlagged(t, got, f.uuidOf("journal_entry", "journal_entry_id", "journal_entry_uuid", outside), "dated outside any closed period")
}
