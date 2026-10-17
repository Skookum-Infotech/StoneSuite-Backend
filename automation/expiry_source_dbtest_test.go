//go:build dbtest

package automation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type expDoc struct {
	cust    int
	owner   *int
	status  string
	valid   *time.Time
	deleted bool
}

// expiryDoc inserts a quote or estimate for spec and returns its uuid.
func (f *arFixture) expiryDoc(spec expirySpec, d expDoc) string {
	var uuid string
	q := fmt.Sprintf(`
		INSERT INTO %[1]s (record_type, %[1]s_status, %[1]s_customer_id, %[1]s_valid_until, %[1]s_owner_id)
		VALUES ($1, $2, $3, $4, $5) RETURNING %[1]s_uuid::text`, spec.Table)
	require.NoError(f.t, f.tx.QueryRow(f.ctx, q, f.typeID(spec.TypeCode), f.statusID(spec.TypeCode, d.status),
		d.cust, d.valid, d.owner).Scan(&uuid))
	if d.deleted {
		_, err := f.tx.Exec(f.ctx, fmt.Sprintf(`UPDATE %[1]s SET %[1]s_deleted_at = now(), %[1]s_deleted_by = 1 WHERE %[1]s_uuid = $1::uuid`, spec.Table), uuid)
		require.NoError(f.t, err)
	}
	return uuid
}

// employeeWithUser creates an employee linked to a fresh user; returns the employee id and user id.
func (f *arFixture) employeeWithUser(email, status string) (int, string) {
	var userID string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO users (identity_id, email, status) VALUES (gen_random_uuid(), $1, $2) RETURNING id::text`,
		email, status).Scan(&userID))
	var empID int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO employee (employee_user_id, employee_email) VALUES ($1::uuid, $1 || '.emp') RETURNING employee_id`,
		userID).Scan(&empID))
	return empID, userID
}

func (f *arFixture) employeeNoUser(email string) int {
	var id int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `INSERT INTO employee (employee_email) VALUES ($1) RETURNING employee_id`, email).Scan(&id))
	return id
}

func TestExpiryListQuoteCandidates(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	emp, userID := f.employeeWithUser("owner-q@x.test", "active")
	orphan := f.employeeNoUser("orphan-q@x.test")
	base := expDoc{cust: cust, owner: &emp, status: "SENT", valid: dayOffset(3)}
	with := func(mut func(*expDoc)) string { d := base; mut(&d); return f.expiryDoc(quoteSpec, d) }

	good := with(func(d *expDoc) {})
	boundaryOld := with(func(d *expDoc) { d.valid = dayOffset(-2) })
	tooOld := with(func(d *expDoc) { d.valid = dayOffset(-3) })
	tooNew := with(func(d *expDoc) { d.valid = dayOffset(4) })
	draft := with(func(d *expDoc) { d.status = "DRFT" })
	nullValid := with(func(d *expDoc) { d.valid = nil })
	deleted := with(func(d *expDoc) { d.deleted = true })
	noUser := with(func(d *expDoc) { d.owner = &orphan })
	noOwner := with(func(d *expDoc) { d.owner = nil })
	offEmp, _ := f.employeeWithUser("off-q@x.test", "disabled")
	offUser := with(func(d *expDoc) { d.owner = &offEmp })
	delEmp, _ := f.employeeWithUser("del-q@x.test", "active")
	_, err0 := f.tx.Exec(f.ctx, `UPDATE employee SET employee_deleted_at = now(), employee_deleted_by = 1 WHERE employee_id = $1`, delEmp)
	require.NoError(t, err0)
	deletedEmp := with(func(d *expDoc) { d.owner = &delEmp })
	converted := with(func(d *expDoc) {})
	f.convert(converted)

	got, err := listExpiryCandidates(f.ctx, f.tx, quoteSpec, arToday)
	require.NoError(t, err)
	by := map[string]ExpiryDoc{}
	for _, d := range got {
		by[d.UUID] = d
	}
	for name, id := range map[string]string{"good": good, "boundaryOld": boundaryOld, "noUser": noUser, "noOwner": noOwner, "offUser": offUser, "deletedEmp": deletedEmp} {
		assert.Contains(t, by, id, name)
	}
	for name, id := range map[string]string{"tooOld": tooOld, "tooNew": tooNew, "draft": draft,
		"nullValid": nullValid, "deleted": deleted, "converted": converted} {
		assert.NotContains(t, by, id, name)
	}
	assert.Equal(t, "owner-q@x.test", by[good].OwnerEmail)
	assert.Equal(t, userID, by[good].OwnerUserID)
	assert.NotEmpty(t, by[good].OwnerIdentityID)
	assert.Equal(t, "Acme", by[good].CustomerName)
	assert.True(t, by[good].ValidUntil.Equal(*dayOffset(3)))
	assert.Empty(t, by[noUser].OwnerEmail)
	assert.Empty(t, by[offUser].OwnerEmail, "a non-active owner user is not a recipient")
	assert.Empty(t, by[offUser].OwnerUserID)
	assert.Empty(t, by[deletedEmp].OwnerEmail, "a deleted owner employee is not a recipient")
	assert.Empty(t, by[noOwner].OwnerUserID)
}

// convert marks a quote converted by creating a sales order and a quote_conversion row.
func (f *arFixture) convert(quoteUUID string) {
	var soID int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id)
		SELECT $1, $2, quote_customer_id FROM quote WHERE quote_uuid = $3::uuid
		RETURNING sales_order_id`, f.typeID("SORD"), f.statusID("SORD", "DRFT"), quoteUUID).Scan(&soID))
	_, err := f.tx.Exec(f.ctx, `
		INSERT INTO quote_conversion (quote_id, sales_order_id)
		SELECT quote_id, $2 FROM quote WHERE quote_uuid = $1::uuid`, quoteUUID, soID)
	require.NoError(f.t, err)
}

func TestExpiryListEstimateCandidates(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	emp, _ := f.employeeWithUser("owner-e@x.test", "active")
	base := expDoc{cust: cust, owner: &emp, status: "SENT", valid: dayOffset(3)}
	with := func(mut func(*expDoc)) string { d := base; mut(&d); return f.expiryDoc(estimateSpec, d) }

	good := with(func(d *expDoc) {})
	draft := with(func(d *expDoc) { d.status = "DRFT" })
	deleted := with(func(d *expDoc) { d.deleted = true })
	linked := with(func(d *expDoc) {})
	deletedQuote := with(func(d *expDoc) {})
	f.linkQuote(linked, false)
	f.linkQuote(deletedQuote, true)

	got, err := listExpiryCandidates(f.ctx, f.tx, estimateSpec, arToday)
	require.NoError(t, err)
	by := map[string]ExpiryDoc{}
	for _, d := range got {
		by[d.UUID] = d
	}
	assert.Contains(t, by, good)
	assert.Contains(t, by, deletedQuote, "a soft-deleted derived quote does not block the estimate")
	assert.NotContains(t, by, draft)
	assert.NotContains(t, by, deleted)
	assert.NotContains(t, by, linked, "estimate with a live derived quote is converted")
	assert.Equal(t, "owner-e@x.test", by[good].OwnerEmail)
}

// linkQuote creates a quote derived from the estimate, optionally soft-deleted.
func (f *arFixture) linkQuote(estimateUUID string, deleted bool) {
	var qid int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO quote (record_type, quote_status, quote_customer_id, quote_estimate_id)
		SELECT $1, $2, estimate_customer_id, estimate_id FROM estimate WHERE estimate_uuid = $3::uuid
		RETURNING quote_id`, f.typeID("QUOT"), f.statusID("QUOT", "DRFT"), estimateUUID).Scan(&qid))
	if deleted {
		_, err := f.tx.Exec(f.ctx, `UPDATE quote SET quote_deleted_at = now(), quote_deleted_by = 1 WHERE quote_id = $1`, qid)
		require.NoError(f.t, err)
	}
}

func TestExpiryEligible(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	emp, userID := f.employeeWithUser("owner-x@x.test", "active")
	valid := dayOffset(3)
	base := expDoc{cust: cust, owner: &emp, status: "SENT", valid: valid}
	with := func(mut func(*expDoc)) string { d := base; mut(&d); return f.expiryDoc(quoteSpec, d) }

	good := with(func(d *expDoc) {})
	conv := with(func(d *expDoc) {})
	f.convert(conv)
	disabledEmp, _ := f.employeeWithUser("off@x.test", "disabled")
	orphan := f.employeeNoUser("orphan-x@x.test")

	tests := []struct {
		name    string
		uuid    string
		planned time.Time
		reason  string
	}{
		{"eligible", good, *valid, ""},
		{"voided (soft-deleted)", with(func(d *expDoc) { d.deleted = true }), *valid, reasonVoided},
		{"unknown document", "00000000-0000-0000-0000-000000000000", *valid, reasonVoided},
		{"converted", conv, *valid, reasonConverted},
		{"not sent", with(func(d *expDoc) { d.status = "DRFT" }), *valid, reasonNotSent},
		{"anchor changed", good, *dayOffset(5), reasonAnchorChanged},
		{"null valid until", with(func(d *expDoc) { d.valid = nil }), *valid, reasonAnchorChanged},
		{"owner user disabled", with(func(d *expDoc) { d.owner = &disabledEmp }), *valid, reasonNoEmail},
		{"owner without user", with(func(d *expDoc) { d.owner = &orphan }), *valid, reasonNoEmail},
		{"no owner", with(func(d *expDoc) { d.owner = nil }), *valid, reasonNoEmail},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, reason, _, err := expiryEligible(f.ctx, f.tx, quoteSpec, tt.uuid, tt.planned)
			require.NoError(t, err)
			assert.Equal(t, tt.reason, reason)
			assert.Equal(t, tt.reason == "", ok)
		})
	}

	// Moving valid_until, then disabling the owner's user, flips an eligible document.
	_, err := f.tx.Exec(f.ctx, `UPDATE quote SET quote_valid_until = $2 WHERE quote_uuid = $1::uuid`, good, dayOffset(6))
	require.NoError(t, err)
	_, reason, live, err := expiryEligible(f.ctx, f.tx, quoteSpec, good, *valid)
	require.NoError(t, err)
	assert.Equal(t, reasonAnchorChanged, reason)
	assert.True(t, live.ValidUntil.Equal(*dayOffset(6)))
	_, err = f.tx.Exec(f.ctx, `UPDATE quote SET quote_valid_until = $2 WHERE quote_uuid = $1::uuid`, good, valid)
	require.NoError(t, err)
	_, err = f.tx.Exec(f.ctx, `UPDATE users SET status = 'disabled' WHERE id = $1::uuid`, userID)
	require.NoError(t, err)
	_, reason, _, err = expiryEligible(f.ctx, f.tx, quoteSpec, good, *valid)
	require.NoError(t, err)
	assert.Equal(t, reasonNoEmail, reason)

	// Estimates share the same query shape.
	est := f.expiryDoc(estimateSpec, expDoc{cust: cust, owner: &emp, status: "SENT", valid: valid})
	f.linkQuote(est, false)
	_, reason, _, err = expiryEligible(f.ctx, f.tx, estimateSpec, est, *valid)
	require.NoError(t, err)
	assert.Equal(t, reasonConverted, reason)
}
