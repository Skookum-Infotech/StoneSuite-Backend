//go:build dbtest

package automation

import (
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (f *arFixture) stateAndRows(ruleID string) (rows int, lastPlanned *string) {
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `SELECT count(*) FROM automation_deliveries`).Scan(&rows))
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `SELECT last_planned_at::text FROM automation_state WHERE rule_id = $1::uuid`, ruleID).Scan(&lastPlanned))
	return rows, lastPlanned
}

func TestPreviewARWritesNothing(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableRule(arToday.AddDate(0, 0, -30))
	cust := f.customer(arCust{accountsEmail: "pay@acme.com"})
	f.invoice(arInv{cust: cust, status: "SENT", due: dayOffset(0), balance: "10", sent: true})

	rowsBefore, lastBefore := f.stateAndRows(rule.ID)
	p, err := previewAR(f.ctx, f.tx, planEnv(), RuleKeyARDunning)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Counts[statusPlanned])
	rowsAfter, lastAfter := f.stateAndRows(rule.ID)
	assert.Equal(t, rowsBefore, rowsAfter)
	assert.Equal(t, lastBefore, lastAfter)
}

func TestPreviewARMatchesPlan(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableRule(arToday.AddDate(0, 0, -30))
	c1 := f.customer(arCust{accountsEmail: "b@acme.com"})
	c2 := f.customer(arCust{accountsEmail: "a@acme.com"})
	f.invoice(arInv{cust: c1, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	f.invoice(arInv{cust: c2, status: "SENT", due: dayOffset(-7), balance: "20", sent: true})

	p, err := previewAR(f.ctx, f.tx, planEnv(), RuleKeyARDunning)
	require.NoError(t, err)
	var got []string
	emails := []string{}
	for _, r := range p.Recipients {
		emails = append(emails, r.Email)
		for _, it := range r.Items {
			got = append(got, it.InvoiceUUID+"/"+strconv.Itoa(it.Stage))
		}
	}
	assert.True(t, sort.StringsAreSorted(emails))

	_, _, err = (&ARDunning{}).planTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	dbRows, err := f.tx.Query(f.ctx, `SELECT record_id || '/' || stage::text FROM automation_deliveries WHERE rule_id = $1::uuid AND status = 'planned'`, rule.ID)
	require.NoError(t, err)
	defer dbRows.Close()
	var want []string
	for dbRows.Next() {
		var s string
		require.NoError(t, dbRows.Scan(&s))
		want = append(want, s)
	}
	require.NoError(t, dbRows.Err())
	sort.Strings(got)
	sort.Strings(want)
	assert.Equal(t, want, got)
	assert.NotEmpty(t, want)

	// After a real plan every row exists: nothing new would be planned.
	after, err := previewAR(f.ctx, f.tx, planEnv(), RuleKeyARDunning)
	require.NoError(t, err)
	assert.Zero(t, after.Counts[statusPlanned])
	assert.Equal(t, len(want), after.Counts[reasonExisting])
	assert.Empty(t, after.Recipients)
}

func TestPreviewARDisabledRuleIgnoresStaleEnabledAt(t *testing.T) {
	f := newARFixture(t)
	f.enableRule(arToday.AddDate(0, 0, -10)) // enabled long ago, then disabled
	_, err := f.tx.Exec(f.ctx, `UPDATE automation_rules SET enabled = FALSE WHERE rule_key = $1`, RuleKeyARDunning)
	require.NoError(t, err)
	cust := f.customer(arCust{accountsEmail: "m@acme.com"})
	f.invoice(arInv{cust: cust, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	p, err := previewAR(f.ctx, f.tx, planEnv(), RuleKeyARDunning)
	require.NoError(t, err)
	assert.Positive(t, p.Counts["pre_enable"])
	assert.Zero(t, p.Counts[statusPlanned])
}

func TestPreviewARGroupsRecipientsCaseInsensitively(t *testing.T) {
	f := newARFixture(t)
	f.enableRule(arToday.AddDate(0, 0, -30))
	c1 := f.customer(arCust{accountsEmail: "Pay@Acme.com"})
	c2 := f.customer(arCust{accountsEmail: "pay@acme.com"})
	f.invoice(arInv{cust: c1, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	f.invoice(arInv{cust: c2, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	p, err := previewAR(f.ctx, f.tx, planEnv(), RuleKeyARDunning)
	require.NoError(t, err)
	require.Len(t, p.Recipients, 1)
	assert.Equal(t, "pay@acme.com", p.Recipients[0].Email)
	assert.Len(t, p.Recipients[0].Items, 2)
}

func TestPreviewARNoEmailAndPreEnableCounts(t *testing.T) {
	f := newARFixture(t)
	f.enableRule(arToday.AddDate(0, 0, -30))
	noMail := f.customer(arCust{})
	f.invoice(arInv{cust: noMail, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	p, err := previewAR(f.ctx, f.tx, planEnv(), RuleKeyARDunning)
	require.NoError(t, err)
	assert.Equal(t, 1, p.Counts[reasonNoEmail])
	assert.Zero(t, p.Counts[statusPlanned])
	assert.Empty(t, p.Recipients)

	// enabled_at NULL: "enabling today", so a stage dated today is pre_enable.
	_, err = f.tx.Exec(f.ctx, `UPDATE automation_state SET enabled_at = NULL WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, RuleKeyARDunning)
	require.NoError(t, err)
	withMail := f.customer(arCust{accountsEmail: "m@acme.com"})
	f.invoice(arInv{cust: withMail, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	p, err = previewAR(f.ctx, f.tx, planEnv(), RuleKeyARDunning)
	require.NoError(t, err)
	assert.Positive(t, p.Counts["pre_enable"])
	assert.Zero(t, p.Counts[statusPlanned])
}
