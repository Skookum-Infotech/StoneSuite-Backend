package vendortransactions

import (
	"strconv"
	"strings"
	"time"
)

// trailingDays is the "last 12 months" window for the paid total.
const trailingDays = 365

// paidStatusCodes are the vendor payment statuses that count as money paid out:
// approved or later in the lifecycle (APPV, SCHD, SENT). This mirrors the AR
// twin in customertransactions, which counts an approved payment as received;
// DRFT and PAPV are not yet committed and VOID never happened.
var paidStatusCodes = []string{codeApproved, codeScheduled, codeSent}

// inWindow is the predicate "doc_date falls in the trailing 12 months".
func inWindow(b *binder) string {
	return "doc_date >= " + b.today() + " - " + strconv.Itoa(trailingDays)
}

// quoteCodes renders status codes as a static SQL list.
func quoteCodes(codes []string) string {
	return "'" + strings.Join(codes, "','") + "'"
}

// buildPayablesSQL assembles the bill-derived figures for one granted vendor
// bill source: open balance and count, and overdue balance and count. They
// reuse the branch's is_open and is_overdue, so the tab and its summary can
// never disagree.
func buildPayablesSQL(bill granted, vendorID, employee int, today time.Time) (string, []any) {
	b := newBinder(vendorID, employee, today)
	branch := bill.branchSQL(b)
	sql := "SELECT COALESCE(SUM(balance) FILTER (WHERE is_open), 0)::float8," +
		" COUNT(*) FILTER (WHERE is_open)," +
		" COALESCE(SUM(balance) FILTER (WHERE is_overdue), 0)::float8," +
		" COUNT(*) FILTER (WHERE is_overdue)" +
		" FROM (" + branch + ") x"
	return sql, b.args
}

// buildCreditsSQL assembles the payment- and credit-derived figures over the
// granted subset of those two sources: unapplied credit (the open balances) and
// the payments made over the trailing 12 months.
func buildCreditsSQL(sources []granted, vendorID, employee int, today time.Time) (string, []any) {
	b := newBinder(vendorID, employee, today)
	union := unionSQL(sources, b)
	sql := "SELECT COALESCE(SUM(balance) FILTER (WHERE is_open), 0)::float8," +
		" COALESCE(SUM(amount) FILTER (WHERE type = '" + keyVendorPayment + "'" +
		" AND status_code IN (" + quoteCodes(paidStatusCodes) + ") AND " + inWindow(b) + "), 0)::float8" +
		" FROM (" + union + ") x"
	return sql, b.args
}

// buildBillPaymentsSQL assembles the total of settlements recorded directly on
// the granted vendor bill source's bills (vendor_bill_payment ledger rows) over
// the trailing 12 months. A payment document applied to a bill is not a ledger
// row, so adding this to the payment documents' total never double-counts. The
// bill's own vendor, soft-delete and own-scope predicates apply.
func buildBillPaymentsSQL(bill granted, vendorID, employee int, today time.Time) (string, []any) {
	b := newBinder(vendorID, employee, today)
	where := []string{
		"p.deleted_at IS NULL",
		"t." + bill.DeletedAt + " IS NULL",
		"t." + bill.VendorCol + " = " + argVendor,
	}
	if bill.own {
		where = append(where, "t."+bill.Owner+" = "+b.emp())
	}
	where = append(where, "p.paid_at >= "+b.today()+" - "+strconv.Itoa(trailingDays))
	sql := "SELECT COALESCE(SUM(p.amount), 0)::float8" +
		" FROM vendor_bill_payment p JOIN " + bill.Table + " t ON t." + bill.Table + "_id = p.vendor_bill_id" +
		" WHERE " + strings.Join(where, " AND ")
	return sql, b.args
}

// buildCountsSQL assembles the per-type, per-status counts over every granted
// source, with each group's open count and latest update for the activity stamp.
func buildCountsSQL(sources []granted, vendorID, employee int, today time.Time) (string, []any) {
	b := newBinder(vendorID, employee, today)
	union := unionSQL(sources, b)
	sql := "SELECT type, status_code, status_name, COUNT(*), COUNT(*) FILTER (WHERE is_open), MAX(updated_at)" +
		" FROM (" + union + ") x GROUP BY type, status_code, status_name"
	return sql, b.args
}
