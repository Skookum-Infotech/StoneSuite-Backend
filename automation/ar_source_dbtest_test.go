//go:build dbtest

package automation

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var arToday = time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)

// arFixture seeds rows inside a rolled-back transaction.
type arFixture struct {
	t   *testing.T
	ctx context.Context
	tx  pgx.Tx
}

func newARFixture(t *testing.T) *arFixture {
	t.Helper()
	pool := newTenantTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return &arFixture{t: t, ctx: ctx, tx: tx}
}

func (f *arFixture) statusID(typeCode, status string) int {
	var id int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		SELECT s.record_status_id FROM lkp_record_status s
		JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
		WHERE t.record_type_code = $1 AND s.record_status_code = $2`, typeCode, status).Scan(&id))
	return id
}

func (f *arFixture) typeID(code string) int {
	var id int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id))
	return id
}

type arCust struct {
	accountsEmail string
	dnc           bool
	arCode        string
	deleted       bool
}

func (f *arFixture) customer(c arCust) int {
	var id int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO customer (record_type, customer_name, customer_accounts_email, customer_do_not_contact,
			customer_ar_status)
		VALUES ($1, 'Acme', $2, $3,
			(SELECT customer_ar_status_id FROM lkp_customer_ar_status WHERE customer_ar_status_code = NULLIF($4, '')))
		RETURNING customer_id`, f.typeID("CUST"), c.accountsEmail, c.dnc, c.arCode).Scan(&id))
	if c.deleted {
		_, err := f.tx.Exec(f.ctx, `UPDATE customer SET customer_deleted_at = now(), customer_deleted_by = 1 WHERE customer_id = $1`, id)
		require.NoError(f.t, err)
	}
	return id
}

type arInv struct {
	cust      int
	status    string
	due       *time.Time
	balance   string
	billEmail string
	sent      bool
	deleted   bool
}

func (f *arFixture) invoice(i arInv) string {
	var uuid string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO invoice (record_type, invoice_status, invoice_customer_id, invoice_due_date,
			invoice_balance_due, invoice_bill_email, invoice_owner_id)
		VALUES ($1, $2, $3, $4, $5::numeric, $6, 1) RETURNING invoice_uuid::text`,
		f.typeID("INVC"), f.statusID("INVC", i.status), i.cust, i.due, i.balance, i.billEmail).Scan(&uuid))
	if i.sent {
		_, err := f.tx.Exec(f.ctx, `INSERT INTO document_sends (record_id, workflow_key, sent_to) VALUES ($1::uuid, 'invoice', 'x@y.com')`, uuid)
		require.NoError(f.t, err)
	}
	if i.deleted {
		_, err := f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_deleted_at = now(), invoice_deleted_by = 1 WHERE invoice_uuid = $1::uuid`, uuid)
		require.NoError(f.t, err)
	}
	return uuid
}

func dayOffset(n int) *time.Time {
	d := arToday.AddDate(0, 0, n)
	return &d
}

func TestARListCandidates(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{accountsEmail: "  Billing@Acme.COM "})
	dncCust := f.customer(arCust{accountsEmail: "d@acme.com", dnc: true})
	holdCust := f.customer(arCust{accountsEmail: "h@acme.com", arCode: "COLL"})
	base := arInv{cust: cust, status: "SENT", due: dayOffset(0), balance: "100.00", sent: true}
	with := func(mut func(*arInv)) string { i := base; mut(&i); return f.invoice(i) }

	good := with(func(i *arInv) { i.billEmail = "bill@acme.com" })
	partial := with(func(i *arInv) { i.status = "PART"; i.balance = "40.50" })
	boundaryOld := with(func(i *arInv) { i.due = dayOffset(-60) })
	boundaryNew := with(func(i *arInv) { i.due = dayOffset(3) })
	dncInv := with(func(i *arInv) { i.cust = dncCust })
	holdInv := with(func(i *arInv) { i.cust = holdCust })
	tooOld := with(func(i *arInv) { i.due = dayOffset(-61) })
	tooNew := with(func(i *arInv) { i.due = dayOffset(4) })
	nullDue := with(func(i *arInv) { i.due = nil })
	draft := with(func(i *arInv) { i.status = "DRFT" })
	paid := with(func(i *arInv) { i.status = "PAID" })
	zero := with(func(i *arInv) { i.balance = "0" })
	voidInv := with(func(i *arInv) { i.status = "VOID" })
	unsent := with(func(i *arInv) { i.sent = false })
	deleted := with(func(i *arInv) { i.deleted = true })
	delCust := with(func(i *arInv) { i.cust = f.customer(arCust{deleted: true}) })

	got, err := listARCandidates(f.ctx, f.tx, arToday)
	require.NoError(t, err)
	byUUID := map[string]ARInvoice{}
	for _, c := range got {
		byUUID[c.UUID] = c
	}
	for name, id := range map[string]string{"good": good, "partial": partial, "boundaryOld": boundaryOld,
		"boundaryNew": boundaryNew, "dnc (planner skips)": dncInv, "hold (planner skips)": holdInv} {
		assert.Contains(t, byUUID, id, name)
	}
	for name, id := range map[string]string{"tooOld": tooOld, "tooNew": tooNew, "nullDue": nullDue, "draft": draft,
		"paid": paid, "zero": zero, "void": voidInv, "unsent": unsent, "deleted": deleted, "customer deleted": delCust} {
		assert.NotContains(t, byUUID, id, name)
	}
	assert.Equal(t, "billing@acme.com", byUUID[partial].Email, "accounts email wins, trimmed + lower-cased")
	assert.Equal(t, "40.50", byUUID[partial].Balance)
	assert.True(t, byUUID[good].Anchor.Equal(arToday), "anchor is the due date")
	assert.Equal(t, "Acme", byUUID[good].CustomerName)
	require.NotNil(t, byUUID[good].OwnerEmployeeID)
	assert.Equal(t, 1, *byUUID[good].OwnerEmployeeID)
}

func TestAREmailPreference(t *testing.T) {
	f := newARFixture(t)
	withAccounts := f.customer(arCust{accountsEmail: "Accounts@X.com"})
	noAccounts := f.customer(arCust{accountsEmail: "   "})
	a := f.invoice(arInv{cust: withAccounts, status: "SENT", due: dayOffset(0), balance: "5", billEmail: "bill@x.com", sent: true})
	b := f.invoice(arInv{cust: noAccounts, status: "SENT", due: dayOffset(0), balance: "5", billEmail: " Bill@X.com ", sent: true})
	got, err := listARCandidates(f.ctx, f.tx, arToday)
	require.NoError(t, err)
	emails := map[string]string{}
	for _, c := range got {
		emails[c.UUID] = c.Email
	}
	assert.Equal(t, "accounts@x.com", emails[a])
	assert.Equal(t, "bill@x.com", emails[b])
}

func TestAREligible(t *testing.T) {
	f := newARFixture(t)
	due := dayOffset(0)
	ok := f.customer(arCust{accountsEmail: "a@x.com"})
	base := arInv{cust: ok, status: "SENT", due: due, balance: "100.00", sent: true}
	with := func(mut func(*arInv)) string { i := base; mut(&i); return f.invoice(i) }
	dncCust := f.customer(arCust{accountsEmail: "a@x.com", dnc: true})
	holdCust := f.customer(arCust{accountsEmail: "a@x.com", arCode: "BDBT"})
	noMail := f.customer(arCust{})

	tests := []struct {
		name   string
		uuid   string
		anchor time.Time
		reason string
	}{
		{"eligible", with(func(i *arInv) {}), *due, ""},
		{"voided (VOID with balance)", with(func(i *arInv) { i.status = "VOID" }), *due, reasonVoided},
		{"voided (soft-deleted)", with(func(i *arInv) { i.deleted = true }), *due, reasonVoided},
		{"paid status", with(func(i *arInv) { i.status = "PAID"; i.balance = "0" }), *due, reasonPaid},
		{"paid via zero balance", with(func(i *arInv) { i.balance = "0" }), *due, reasonPaid},
		{"do not contact", with(func(i *arInv) { i.cust = dncCust }), *due, reasonDoNotContact},
		{"ar hold", with(func(i *arInv) { i.cust = holdCust }), *due, reasonARHold},
		{"not sent", with(func(i *arInv) { i.sent = false }), *due, reasonNotSent},
		{"anchor changed", with(func(i *arInv) {}), *dayOffset(5), reasonAnchorChanged},
		{"no email", with(func(i *arInv) { i.cust = noMail }), *due, reasonNoEmail},
		{"unknown invoice", "00000000-0000-0000-0000-000000000000", *due, reasonVoided},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			okv, reason, _, err := arEligible(f.ctx, f.tx, tt.uuid, tt.anchor, arToday)
			require.NoError(t, err)
			assert.Equal(t, tt.reason, reason)
			assert.Equal(t, tt.reason == "", okv)
		})
	}
}

func TestAREligibleLiveBalanceAndRevert(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{accountsEmail: "a@x.com"})
	due := dayOffset(0)
	id := f.invoice(arInv{cust: cust, status: "SENT", due: due, balance: "100.00", sent: true})

	ok, reason, live, err := arEligible(f.ctx, f.tx, id, *due, arToday)
	require.NoError(t, err)
	assert.True(t, ok, reason)
	assert.Equal(t, "100.00", live.Balance)

	_, err = f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_balance_due = 60, invoice_status = $2 WHERE invoice_uuid = $1::uuid`,
		id, f.statusID("INVC", "PART"))
	require.NoError(t, err)
	ok, _, live, err = arEligible(f.ctx, f.tx, id, *due, arToday)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "60.00", live.Balance, "live balance after a partial payment")

	_, err = f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_balance_due = 0, invoice_status = $2 WHERE invoice_uuid = $1::uuid`,
		id, f.statusID("INVC", "PAID"))
	require.NoError(t, err)
	ok, reason, _, err = arEligible(f.ctx, f.tx, id, *due, arToday)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, reasonPaid, reason)

	_, err = f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_balance_due = 60, invoice_status = $2 WHERE invoice_uuid = $1::uuid`,
		id, f.statusID("INVC", "PART"))
	require.NoError(t, err)
	ok, reason, _, err = arEligible(f.ctx, f.tx, id, *due, arToday)
	require.NoError(t, err)
	assert.True(t, ok, "PAID->PART revert is eligible again: %s", reason)
}

func TestAREligibleCreditCovers(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{accountsEmail: "a@x.com"})
	due := dayOffset(0)
	id := f.invoice(arInv{cust: cust, status: "SENT", due: due, balance: "100.00", sent: true})

	var methodID int
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT payment_method_id FROM lkp_payment_method LIMIT 1`).Scan(&methodID))
	pay := func(status, unapplied, refunded, credited string) {
		_, err := f.tx.Exec(f.ctx, `
			INSERT INTO payment (record_type, payment_status, payment_customer_id, payment_method, payment_amount,
				payment_unapplied_amount, payment_refunded_total, payment_credited_total)
			VALUES ($1, $2, $3, $4, 1000, $5::numeric, $6::numeric, $7::numeric)`,
			f.typeID("PYMT"), f.statusID("PYMT", status), cust, methodID, unapplied, refunded, credited)
		require.NoError(t, err)
	}
	memo := func(status, unapplied string) {
		_, err := f.tx.Exec(f.ctx, `
			INSERT INTO credit_memo (record_type, credit_memo_status, credit_memo_customer_id, credit_memo_unapplied_amount)
			VALUES ($1, $2, $3, $4::numeric)`, f.typeID("CRDT"), f.statusID("CRDT", status), cust, unapplied)
		require.NoError(t, err)
	}
	check := func(want string) {
		t.Helper()
		_, reason, _, err := arEligible(f.ctx, f.tx, id, *due, arToday)
		require.NoError(t, err)
		assert.Equal(t, want, reason)
	}

	pay("VOID", "500", "0", "0") // voided payment never counts
	memo("VOID", "500")          // non-appliable memo never counts
	check("")
	pay("APPV", "100", "30", "0") // 70 available
	check("")
	memo("APPV", "29.99") // 99.99 < 100
	check("")
	memo("APPV", "0.01") // exactly 100 -> covered
	check(reasonCreditCovers)
}
