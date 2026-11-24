//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

func vbE2EEnv(t *testing.T) JobEnv {
	t.Helper()
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	now := time.Date(arToday.Year(), arToday.Month(), arToday.Day(), 10, 0, 0, 0, chicago)
	return JobEnv{Tenant: &tenancy.Tenant{ID: "tenant-vb"}, Now: now, Today: arToday, Loc: chicago}
}

// seededRule loads a seeded system rule and moves its enabled_at a week back so no stage is pre_enable.
func (f *arFixture) vbSeededRule(key string) ruleRow {
	_, err := f.tx.Exec(f.ctx, `UPDATE automation_state SET enabled_at = $2 WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, key, arToday.AddDate(0, 0, -7))
	require.NoError(f.t, err)
	rule, enabled, err := loadRule(f.ctx, f.tx, key)
	require.NoError(f.t, err)
	require.True(f.t, enabled, "the %s rule must be seeded enabled", key)
	return rule
}

func (f *arFixture) vbClaim(ruleID string, env JobEnv) []Group {
	ds, err := claimDue(f.ctx, f.tx, ruleID, env.Now)
	require.NoError(f.t, err)
	return groupDeliveries(ds)
}

func TestVendorBillReminderEndToEnd(t *testing.T) {
	f := newARFixture(t)
	env := vbE2EEnv(t)
	vendor := f.vendor()
	owner, _ := f.employeeWithUser("vb-e2e@x.io", "active")
	rule := f.vbSeededRule(RuleKeyVendorBillDue)
	assert.Equal(t, []ARCadenceStage{{OffsetDays: -3}, {OffsetDays: 0}}, rule.Config.Cadence, "the seeded cadence")

	due3 := f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(3), total: "1250.50", paid: "0", credit: "0", owner: &owner})
	f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(3), total: "10", paid: "10", credit: "0", owner: &owner}) // nothing owed

	planned, skipped, err := planVendorBillsTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, planned, "the -3 stage is due today for the bill due in 3 days")
	assert.Zero(t, skipped)
	again, _, err := planVendorBillsTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Zero(t, again, "re-planning inserts nothing")

	groups := f.vbClaim(rule.ID, env)
	require.Len(t, groups, 1)
	assert.Equal(t, "vb-e2e@x.io", groups[0].RecipientEmail)

	r := vendorBillRenderer{pool: f.tx}
	req, kept, dropped, err := r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Empty(t, dropped)
	require.Len(t, kept, 1)
	assert.Equal(t, vendorBillTitle, req.Title)
	assert.Equal(t, due3, req.ResourceID)
	assert.Equal(t, "1 vendor bill needs payment.", req.Body)
	require.NotNil(t, req.Email)
	require.Len(t, req.Email.Details, 1)
	assert.Contains(t, req.Email.Details[0].Value, "$1,250.50")
	assert.Contains(t, req.Email.Details[0].Value, "due in 3 days")
	assert.NotEmpty(t, req.Link, "the notification links to the bill in the app")

	// Paid before the send: dropped, not sent.
	_, err = f.tx.Exec(f.ctx, `UPDATE vendor_bill SET vendor_bill_status = $2, vendor_bill_amount_paid = 1250.50 WHERE vendor_bill_uuid = $1::uuid`, due3, f.statusID("VBIL", "PAID"))
	require.NoError(t, err)
	_, kept, dropped, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Empty(t, kept)
	assert.Equal(t, reasonPaid, dropped[groups[0].Items[0].OccurrenceKey])

	// Reassigned to someone else: the original owner is no longer the recipient.
	other, _ := f.employeeWithUser("vb-other@x.io", "active")
	_, err = f.tx.Exec(f.ctx, `UPDATE vendor_bill SET vendor_bill_status = $2, vendor_bill_amount_paid = 0, vendor_bill_owner_id = $3 WHERE vendor_bill_uuid = $1::uuid`, due3, f.statusID("VBIL", "APPV"), other)
	require.NoError(t, err)
	_, _, dropped, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Equal(t, reasonRecipientChanged, dropped[groups[0].Items[0].OccurrenceKey])
}
