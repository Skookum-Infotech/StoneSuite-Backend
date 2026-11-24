//go:build dbtest

package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *arFixture) optIn(userID string, mutate func(*DigestPrefs)) DigestPrefs {
	p := DefaultDigestPrefs()
	p.Enabled = true
	if mutate != nil {
		mutate(&p)
	}
	require.NoError(f.t, SaveDigestPrefs(f.ctx, f.tx, userID, p))
	return p
}

func TestDigestPrefsRoundTrip(t *testing.T) {
	f := newARFixture(t)
	_, userID := f.employeeWithUser("prefs@x.io", "active")

	got, err := LoadDigestPrefs(f.ctx, f.tx, userID)
	require.NoError(t, err)
	assert.Equal(t, DefaultDigestPrefs(), got, "a user with no row has the defaults, digest off")

	want := DigestPrefs{Enabled: true, SendHour: 14, IncludeApprovals: false, IncludeInvoices: true, IncludeExpiring: true, SkipEmpty: false}
	require.NoError(t, SaveDigestPrefs(f.ctx, f.tx, userID, want))
	got, err = LoadDigestPrefs(f.ctx, f.tx, userID)
	require.NoError(t, err)
	assert.Equal(t, want, got)

	want.SendHour = 9
	require.NoError(t, SaveDigestPrefs(f.ctx, f.tx, userID, want), "saving again updates the same row")
	got, err = LoadDigestPrefs(f.ctx, f.tx, userID)
	require.NoError(t, err)
	assert.Equal(t, 9, got.SendHour)

	var rows int
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT COUNT(*) FROM automation_digest_prefs WHERE user_id = $1::uuid`, userID).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestDigestPrefsAreRefusedBeforeTheDatabase(t *testing.T) {
	f := newARFixture(t)
	_, userID := f.employeeWithUser("bad@x.io", "active")
	bad := DefaultDigestPrefs()
	bad.SendHour = 3
	assert.ErrorIs(t, SaveDigestPrefs(f.ctx, f.tx, userID, bad), ErrInvalidDigestPrefs)
	var rows int
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT COUNT(*) FROM automation_digest_prefs WHERE user_id = $1::uuid`, userID).Scan(&rows))
	assert.Zero(t, rows, "an invalid save must write nothing")
}

func TestDigestHourConstraintBacksUpTheCode(t *testing.T) {
	f := newARFixture(t)
	_, userID := f.employeeWithUser("constraint@x.io", "active")
	for _, hour := range []int{7, 18, -1, 24} {
		_, err := f.tx.Exec(f.ctx, `SAVEPOINT s`)
		require.NoError(t, err)
		_, err = f.tx.Exec(f.ctx, `INSERT INTO automation_digest_prefs (user_id, send_hour) VALUES ($1::uuid, $2)`, userID, hour)
		assert.Error(t, err, "hour %d must be refused by the table itself", hour)
		_, err = f.tx.Exec(f.ctx, `ROLLBACK TO SAVEPOINT s`)
		require.NoError(t, err)
	}
}

func TestListDigestRecipients(t *testing.T) {
	f := newARFixture(t)
	empOn, userOn := f.employeeWithUser("on@x.io", "active")
	_, userOff := f.employeeWithUser("off@x.io", "active")
	_, userDisabled := f.employeeWithUser("disabled@x.io", "disabled")
	var userNoEmp string
	require.NoError(t, f.tx.QueryRow(f.ctx, `INSERT INTO users (identity_id, email, status) VALUES (gen_random_uuid(), 'noemp@x.io', 'active') RETURNING id::text`).Scan(&userNoEmp))

	f.optIn(userOn, func(p *DigestPrefs) { p.SendHour = 11 })
	f.optIn(userDisabled, nil)
	f.optIn(userNoEmp, nil)
	require.NoError(t, SaveDigestPrefs(f.ctx, f.tx, userOff, DefaultDigestPrefs()), "a saved but switched-off row")

	got, err := listDigestRecipients(f.ctx, f.tx)
	require.NoError(t, err)
	byEmail := map[string]DigestRecipient{}
	for _, r := range got {
		byEmail[r.Email] = r
	}
	require.Len(t, byEmail, 2, "only opted-in, active users are listed")
	assert.Equal(t, empOn, byEmail["on@x.io"].EmployeeID)
	assert.Equal(t, 11, byEmail["on@x.io"].Prefs.SendHour)
	assert.Zero(t, byEmail["noemp@x.io"].EmployeeID, "a user with no employee record is still listed, with employee 0")

	one, found, err := loadDigestRecipient(f.ctx, f.tx, userOn)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, "on@x.io", one.Email)
	off, found, err := loadDigestRecipient(f.ctx, f.tx, userOff)
	require.NoError(t, err)
	require.True(t, found)
	assert.False(t, off.Prefs.Enabled, "the live load returns a switched-off user so the renderer can drop the digest")
	_, found, err = loadDigestRecipient(f.ctx, f.tx, userDisabled)
	require.NoError(t, err)
	assert.False(t, found, "a deactivated user is gone")
}

func TestDigestOverdueInvoiceSummary(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	mine, _ := f.employeeWithUser("inv-mine@x.io", "active")
	other, _ := f.employeeWithUser("inv-other@x.io", "active")
	mk := func(owner int, i arInv) {
		i.cust = cust
		uuid := f.invoice(i)
		_, err := f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_owner_id = $2 WHERE invoice_uuid = $1::uuid`, uuid, owner)
		require.NoError(t, err)
	}
	mk(mine, arInv{status: "SENT", due: dayOffset(-5), balance: "100.50"})
	mk(mine, arInv{status: "ODUE", due: dayOffset(-30), balance: "200"})
	mk(mine, arInv{status: "PART", due: dayOffset(-1), balance: "50"})
	mk(mine, arInv{status: "SENT", due: dayOffset(0), balance: "999"}) // due today is not yet overdue
	mk(mine, arInv{status: "SENT", due: dayOffset(4), balance: "999"}) // not due yet
	mk(mine, arInv{status: "PAID", due: dayOffset(-9), balance: "0"})  // settled
	mk(mine, arInv{status: "SENT", due: dayOffset(-9), balance: "0"})  // nothing owed
	mk(mine, arInv{status: "SENT", due: dayOffset(-9), balance: "70", deleted: true})
	mk(other, arInv{status: "SENT", due: dayOffset(-5), balance: "1000"})

	n, total, err := overdueInvoiceSummary(f.ctx, f.tx, mine, arToday)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.InDelta(t, 350.50, total, 0.001)

	n, total, err = overdueInvoiceSummary(f.ctx, f.tx, other, arToday)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "each user sees only their own invoices")
	assert.InDelta(t, 1000, total, 0.001)

	n, total, err = overdueInvoiceSummary(f.ctx, f.tx, 999999, arToday)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Zero(t, total, "no rows still yields zero, not a scan error")
}

func TestDigestExpiringCount(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	mine, _ := f.employeeWithUser("exp-mine@x.io", "active")
	other, _ := f.employeeWithUser("exp-other@x.io", "active")

	f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &mine, status: "SENT", valid: dayOffset(0)})
	f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &mine, status: "SENT", valid: dayOffset(7)})
	f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &mine, status: "SENT", valid: dayOffset(8)})  // beyond the horizon
	f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &mine, status: "SENT", valid: dayOffset(-1)}) // already expired
	f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &mine, status: "DRFT", valid: dayOffset(3)})
	f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &mine, status: "SENT", valid: dayOffset(3), deleted: true})
	f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &other, status: "SENT", valid: dayOffset(3)})
	converted := f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &mine, status: "SENT", valid: dayOffset(3)})
	f.convert(converted)
	f.expiryDoc(estimateSpec, expDoc{cust: cust, owner: &mine, status: "SENT", valid: dayOffset(2)})
	linked := f.expiryDoc(estimateSpec, expDoc{cust: cust, owner: &mine, status: "SENT", valid: dayOffset(2)})
	f.linkQuote(linked, false)

	q, err := expiringCount(f.ctx, f.tx, quoteSpec, mine, arToday)
	require.NoError(t, err)
	assert.Equal(t, 2, q, "today and the last day of the horizon count; converted, draft, deleted, others' and out-of-range do not")
	e, err := expiringCount(f.ctx, f.tx, estimateSpec, mine, arToday)
	require.NoError(t, err)
	assert.Equal(t, 1, e, "an estimate that already has a quote is not counted")
	q, err = expiringCount(f.ctx, f.tx, quoteSpec, other, arToday)
	require.NoError(t, err)
	assert.Equal(t, 1, q)
}

func TestBuildDigestContent(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	emp, userID := f.employeeWithUser("content@x.io", "active")
	uuid := f.invoice(arInv{cust: cust, status: "SENT", due: dayOffset(-5), balance: "80"})
	_, err := f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_owner_id = $2 WHERE invoice_uuid = $1::uuid`, uuid, emp)
	require.NoError(t, err)
	f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &emp, status: "SENT", valid: dayOffset(2)})

	rc, found, err := loadDigestRecipient(f.ctx, f.tx, userID)
	require.NoError(t, err)
	require.False(t, found, "no prefs row yet")
	f.optIn(userID, nil)
	rc, found, err = loadDigestRecipient(f.ctx, f.tx, userID)
	require.NoError(t, err)
	require.True(t, found)

	c, err := buildDigestContent(f.ctx, f.tx, rc, arToday)
	require.NoError(t, err, "a user with no approver grants must not make the approvals count fail")
	assert.Zero(t, c.Approvals)
	assert.Equal(t, 1, c.OverdueInvoices)
	assert.InDelta(t, 80, c.OverdueTotal, 0.001)
	assert.Equal(t, 1, c.ExpiringQuotes)
	assert.Zero(t, c.ExpiringEstimates)
	assert.False(t, c.empty())

	rc.Prefs.IncludeInvoices, rc.Prefs.IncludeExpiring = false, false
	c, err = buildDigestContent(f.ctx, f.tx, rc, arToday)
	require.NoError(t, err)
	assert.True(t, c.empty(), "a section the user switched off is never queried or shown")

	rc.EmployeeID = 0
	rc.Prefs = DefaultDigestPrefs()
	c, err = buildDigestContent(f.ctx, f.tx, rc, arToday)
	require.NoError(t, err)
	assert.True(t, c.empty(), "a user with no employee record owns nothing")
}
