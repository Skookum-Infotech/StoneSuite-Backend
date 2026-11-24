//go:build dbtest

package automation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/approvalchain"
)

// salesDoc names one of the three documents that get stuck-approval reminders through
// ReminderName: its workflow key, record type, table, and the label its reminder uses.
type salesDoc struct {
	key, typeCode, table, label string
}

var salesDocs = []salesDoc{
	{"quote", "QUOT", "quote", "Quote"},
	{"estimate", "ESTM", "estimate", "Estimate"},
	{"sales_order", "SORD", "sales_order", "Sales Order"},
}

// salesDocAtGate inserts one document of d sitting at its PAPV approval gate since entered,
// owned by owner, with approver as its configured approver. It returns the document's uuid.
func (f *arFixture) salesDocAtGate(d salesDoc, cust, owner, approver int, entered time.Time) string {
	var id int
	var uuid string
	q := fmt.Sprintf(`
		INSERT INTO %[1]s (record_type, %[1]s_status, %[1]s_approval_status, %[1]s_customer_id, %[1]s_owner_id, %[1]s_number)
		VALUES ($1, $2, 'pending', $3, $4, $5 || '-' || substr(gen_random_uuid()::text, 1, 8))
		RETURNING %[1]s_uuid::text, %[1]s_id`, d.table)
	require.NoError(f.t, f.tx.QueryRow(f.ctx, q, f.typeID(d.typeCode), f.statusID(d.typeCode, "PAPV"), cust, owner, d.typeCode).Scan(&uuid, &id))
	_, err := f.tx.Exec(f.ctx, fmt.Sprintf(`INSERT INTO %[1]s_history (%[1]s_id, to_status_id, at) VALUES ($1, $2, $3)`, d.table),
		id, f.statusID(d.typeCode, "PAPV"), entered)
	require.NoError(f.t, err)
	_, err = f.tx.Exec(f.ctx, fmt.Sprintf(`INSERT INTO %[1]s_approver (record_type_id, record_status_id, approver_employee_id) VALUES ($1, $2, $3)`, d.table),
		f.typeID(d.typeCode), f.statusID(d.typeCode, "PAPV"), approver)
	require.NoError(f.t, err)
	return uuid
}

func TestStuckApprovalRemindersCoverSalesDocuments(t *testing.T) {
	for _, d := range salesDocs {
		t.Run(d.key, func(t *testing.T) {
			setFrontend(t)
			f := newARFixture(t)
			rule := f.enableApprovalRule(arToday.AddDate(0, 0, -30))
			cust := f.customer(arCust{})
			approver, _ := f.employeeWithUser("approver-"+d.key+"@x.test", "active")
			uuid := f.salesDocAtGate(d, cust, approver, approver, atDays(3, 12))

			planned, skipped, err := planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
			require.NoError(t, err)
			assert.Equal(t, 1, planned, "a %s waiting 3 days gets the +2 day reminder", d.key)
			assert.Zero(t, skipped)

			var resource, recordID, email string
			require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT resource, record_id, recipient_email FROM automation_deliveries
				WHERE rule_id = $1::uuid AND status = 'planned'`, rule.ID).Scan(&resource, &recordID, &email))
			assert.Equal(t, d.key, resource)
			assert.Equal(t, uuid, recordID)
			assert.Equal(t, "approver-"+d.key+"@x.test", email)

			again, _, err := planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
			require.NoError(t, err)
			assert.Zero(t, again, "re-planning inserts nothing")

			g := Group{DigestKey: "d", RecipientEmail: email, Items: []Delivery{delivery(d.key, uuid, atDays(3, 0))}}
			req, kept, dropped, err := approvalRenderer{q: f.tx}.Render(f.ctx, renderEnv(time.UTC, arToday), g)
			require.NoError(t, err)
			require.Len(t, kept, 1, "dropped: %v", dropped)
			assert.Equal(t, "Approval reminder", req.Title)
			assert.Equal(t, d.key, req.Resource)
			assert.Equal(t, "/sales/"+d.key+"/"+uuid, req.Link, "the reminder links to the document in the app")
			require.Len(t, req.Email.Details, 1)
			assert.Contains(t, req.Email.Details[0].Label, d.label+" "+d.typeCode+"-", "the email names the document type")
			assert.Equal(t, "waiting 3 days", req.Email.Details[0].Value)

			// It leaves the approval gate before the send: dropped, not sent.
			_, err = f.tx.Exec(f.ctx, fmt.Sprintf(`UPDATE %[1]s SET %[1]s_status = $2 WHERE %[1]s_uuid = $1::uuid`, d.table), uuid, f.statusID(d.typeCode, "APPV"))
			require.NoError(t, err)
			_, kept, dropped, err = approvalRenderer{q: f.tx}.Render(f.ctx, renderEnv(time.UTC, arToday), g)
			require.NoError(t, err)
			assert.Empty(t, kept)
			assert.Equal(t, reasonNotPending, dropped[g.Items[0].OccurrenceKey])
		})
	}
}

// Only the reminders are on: approving, rejecting and submitting these documents must still
// send no approval notification, and the anomaly rule must still skip them.
func TestSalesDocumentsStayNotificationDark(t *testing.T) {
	for _, d := range salesDocs {
		cfg, ok := approvalchain.ForWorkflowKey(d.key)
		require.True(t, ok, d.key)
		assert.Empty(t, cfg.DisplayName, "%s: DisplayName gates every approval notification", d.key)
		assert.Empty(t, cfg.Resource, "%s: Resource gates every approval notification", d.key)
	}
}
