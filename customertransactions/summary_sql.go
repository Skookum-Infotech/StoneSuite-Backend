package customertransactions

import (
	"strconv"
	"strings"
	"time"
)

// Keys of the sources the summary reads by name.
const (
	keyInvoice    = "invoice"
	keyPayment    = "payment"
	keyCreditMemo = "credit_memo"
)

// trailingDays is the "last 12 months" window for billed and paid totals.
const trailingDays = 365

// billedStatusCodes are the invoice statuses that count as billed: SENT or later
// in the lifecycle (invoice/revenue.go billedStatusCodes).
var billedStatusCodes = []string{codeSent, codePartial, codeOverdue, "PAID"}

// paidStatusCodes are the payment statuses that count as money received.
var paidStatusCodes = []string{codeApproved, "DEPO"}

// inWindow is the predicate "doc_date falls in the trailing 12 months".
func inWindow(b *binder) string {
	return "doc_date >= " + b.today() + " - " + strconv.Itoa(trailingDays)
}

// quoteCodes renders status codes as a static SQL list.
func quoteCodes(codes []string) string {
	return "'" + strings.Join(codes, "','") + "'"
}

// buildReceivablesSQL assembles the invoice-derived figures for one granted
// invoice source: open balance and count, overdue balance and count, and the
// billed total over the trailing 12 months. They reuse the branch's is_open and
// is_overdue, so the tab and its summary can never disagree.
func buildReceivablesSQL(inv granted, customerID, employee int, today time.Time) (string, []any) {
	b := newBinder(customerID, employee, today)
	branch := inv.branchSQL(b)
	sql := "SELECT COALESCE(SUM(balance) FILTER (WHERE is_open), 0)::float8," +
		" COUNT(*) FILTER (WHERE is_open)," +
		" COALESCE(SUM(balance) FILTER (WHERE is_overdue), 0)::float8," +
		" COUNT(*) FILTER (WHERE is_overdue)," +
		" COALESCE(SUM(amount) FILTER (WHERE status_code IN (" + quoteCodes(billedStatusCodes) + ") AND " + inWindow(b) + "), 0)::float8" +
		" FROM (" + branch + ") x"
	return sql, b.args
}

// buildCreditsSQL assembles the payment- and credit-memo-derived figures over the
// granted subset of those two sources: unapplied credit (the open balances) and
// the payments received over the trailing 12 months.
func buildCreditsSQL(sources []granted, customerID, employee int, today time.Time) (string, []any) {
	b := newBinder(customerID, employee, today)
	union := unionSQL(sources, b)
	sql := "SELECT COALESCE(SUM(balance) FILTER (WHERE is_open), 0)::float8," +
		" COALESCE(SUM(amount) FILTER (WHERE type = '" + keyPayment + "'" +
		" AND status_code IN (" + quoteCodes(paidStatusCodes) + ") AND " + inWindow(b) + "), 0)::float8" +
		" FROM (" + union + ") x"
	return sql, b.args
}

// buildCountsSQL assembles the per-type, per-status counts over every granted
// source, with each group's open count and latest update for the activity stamp.
func buildCountsSQL(sources []granted, customerID, employee int, today time.Time) (string, []any) {
	b := newBinder(customerID, employee, today)
	union := unionSQL(sources, b)
	sql := "SELECT type, status_code, status_name, COUNT(*), COUNT(*) FILTER (WHERE is_open), MAX(updated_at)" +
		" FROM (" + union + ") x GROUP BY type, status_code, status_name"
	return sql, b.args
}
