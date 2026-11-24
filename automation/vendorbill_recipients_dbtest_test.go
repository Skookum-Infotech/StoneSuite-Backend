//go:build dbtest

package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vbApprover configures employee as an active vendor bill approver at the PAPV gate.
func (f *arFixture) vbApprover(emp int) {
	_, err := f.tx.Exec(f.ctx, `INSERT INTO vendor_bill_approver (record_type_id, record_status_id, approver_employee_id) VALUES ($1, $2, $3)`,
		f.typeID("VBIL"), f.statusID("VBIL", "PAPV"), emp)
	require.NoError(f.t, err)
}

func emailsOf(rs []vendorBillRecipient) []string {
	out := []string{}
	for _, r := range rs {
		out = append(out, r.Email)
	}
	return out
}

func TestVendorBillRecipients(t *testing.T) {
	f := newARFixture(t)
	vendor := f.vendor()
	owner, _ := f.employeeWithUser("vb-owner@x.io", "active")
	appA, _ := f.employeeWithUser("approver-a@x.io", "active")
	appB, _ := f.employeeWithUser("approver-b@x.io", "active")
	appOff, _ := f.employeeWithUser("approver-off@x.io", "disabled")
	for _, e := range []int{appA, appB, appOff} {
		f.vbApprover(e)
	}
	mk := func(owner *int) VendorBillDoc {
		uuid := f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(3), total: "100", paid: "0", credit: "0", owner: owner})
		docs, err := listVendorBillCandidates(f.ctx, f.tx, arToday)
		require.NoError(t, err)
		for _, d := range docs {
			if d.UUID == uuid {
				return d
			}
		}
		require.FailNow(t, "bill not listed")
		return VendorBillDoc{}
	}

	t.Run("the owner, when the owner is an active user", func(t *testing.T) {
		got, err := vendorBillRecipients(f.ctx, f.tx, mk(&owner))
		require.NoError(t, err)
		assert.Equal(t, []string{"vb-owner@x.io"}, emailsOf(got), "approvers are not added when there is an owner")
		assert.NotEmpty(t, got[0].UserID)
		assert.NotEmpty(t, got[0].IdentityID, "the identity id is what puts the notification in the user's in-app feed")
	})

	t.Run("the bill's approvers when it has no owner", func(t *testing.T) {
		got, err := vendorBillRecipients(f.ctx, f.tx, mk(nil))
		require.NoError(t, err)
		assert.Equal(t, []string{"approver-a@x.io", "approver-b@x.io"}, emailsOf(got),
			"every approver who can still act, once each; a deactivated approver is left out")
		for _, r := range got {
			assert.NotEmpty(t, r.IdentityID)
		}
	})

	t.Run("the approvers when the owner has been deactivated", func(t *testing.T) {
		gone, _ := f.employeeWithUser("former-owner@x.io", "disabled")
		got, err := vendorBillRecipients(f.ctx, f.tx, mk(&gone))
		require.NoError(t, err)
		assert.Equal(t, []string{"approver-a@x.io", "approver-b@x.io"}, emailsOf(got))
	})

	t.Run("nobody when there is no owner and no approver can act", func(t *testing.T) {
		_, err := f.tx.Exec(f.ctx, `UPDATE vendor_bill_approver SET is_active = FALSE`)
		require.NoError(t, err)
		got, err := vendorBillRecipients(f.ctx, f.tx, mk(nil))
		require.NoError(t, err)
		assert.Empty(t, got, "the planner records such a bill as skipped (no_email) so the delivery log says why")
	})
}

func TestVendorBillReminderFallsBackToApproversEndToEnd(t *testing.T) {
	f := newARFixture(t)
	env := vbE2EEnv(t)
	vendor := f.vendor()
	appA, _ := f.employeeWithUser("fa@x.io", "active")
	appB, _ := f.employeeWithUser("fb@x.io", "active")
	f.vbApprover(appA)
	f.vbApprover(appB)
	rule := f.vbSeededRule(RuleKeyVendorBillDue)
	bill := f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(3), total: "980", paid: "0", credit: "0"}) // no owner

	planned, skipped, err := planVendorBillsTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Equal(t, 2, planned, "one reminder per approver")
	assert.Zero(t, skipped)
	again, _, err := planVendorBillsTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Zero(t, again, "re-planning inserts nothing")

	groups := f.vbClaim(rule.ID, env)
	require.Len(t, groups, 2, "each approver gets their own email")
	for _, g := range groups {
		req, kept, dropped, err := vendorBillRenderer{pool: f.tx}.Render(f.ctx, env, g)
		require.NoError(t, err)
		assert.Empty(t, dropped)
		require.Len(t, kept, 1)
		assert.Equal(t, []string{arChannelMail, expiryChannelApp}, req.Channels, "email and in-app")
		assert.NotEmpty(t, req.Recipients[0].UserID, "an in-app notification needs the recipient's id")
		assert.Equal(t, g.RecipientEmail, req.Recipients[0].Email)
		assert.Equal(t, bill, req.ResourceID)
		assert.Equal(t, "/purchases/vendor_bill/"+bill, req.Link, "it opens the bill")
	}

	// An approver is deactivated before the send: only that recipient is dropped.
	_, err = f.tx.Exec(f.ctx, `UPDATE users SET status = 'disabled' WHERE email = 'fb@x.io'`)
	require.NoError(t, err)
	for _, g := range groups {
		_, kept, dropped, err := vendorBillRenderer{pool: f.tx}.Render(f.ctx, env, g)
		require.NoError(t, err)
		if g.RecipientEmail == "fb@x.io" {
			assert.Empty(t, kept)
			assert.Equal(t, reasonRecipientChanged, dropped[g.Items[0].OccurrenceKey])
		} else {
			assert.Len(t, kept, 1, "the other approver is unaffected")
		}
	}
}

func TestVendorBillWithNobodyResponsibleIsSkippedAndSaysWhy(t *testing.T) {
	f := newARFixture(t)
	env := vbE2EEnv(t)
	vendor := f.vendor()
	rule := f.vbSeededRule(RuleKeyVendorBillDue)
	f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(3), total: "50", paid: "0", credit: "0"}) // no owner, no approvers

	planned, skipped, err := planVendorBillsTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Zero(t, planned)
	assert.Equal(t, 1, skipped)
	var reason string
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT reason FROM automation_deliveries WHERE rule_id = $1::uuid`, rule.ID).Scan(&reason))
	assert.Equal(t, reasonNoEmail, reason)
	assert.Empty(t, f.vbClaim(rule.ID, env))
}
