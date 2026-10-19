//go:build dbtest

package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

func TestApprovalRenderer(t *testing.T) {
	f := newARFixture(t)
	a, _ := f.employeeWithUser("a@x.test", "active")
	b, _ := f.employeeWithUser("b@x.test", "active")
	f.approver(a)
	f.approver(b)
	env := JobEnv{Tenant: &tenancy.Tenant{ID: "t1"}, Today: arToday, Loc: arToday.Location()}
	spec := poSpec(t)

	live, _ := f.po(apprPO{owner: &a, entered: atDays(6, 12)})
	newer, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	gone, goneID := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_status = $2 WHERE purchase_order_id = $1`, goneID, f.statusID("PORD", "APPV"))
	require.NoError(t, err)
	signed, signedID := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	f.signOff(signedID, a) // a already signed: no longer a recipient for this record

	item := func(uuid string, days int) Delivery {
		return Delivery{Resource: spec.Workflow, RecordID: uuid, OccurrenceKey: "k-" + uuid, RecipientEmail: "a@x.test",
			RecipientKind: recipientInternal, Anchor: arToday.AddDate(0, 0, -days)}
	}
	g := Group{DigestKey: "d", RecipientEmail: "A@x.test", Items: []Delivery{item(newer, 3), item(live, 6), item(gone, 3), item(signed, 3)}}
	req, kept, dropped, err := approvalRenderer{q: f.tx}.Render(f.ctx, env, g)
	require.NoError(t, err)
	require.Len(t, kept, 2)
	assert.Equal(t, reasonNotPending, dropped["k-"+gone])
	assert.Equal(t, reasonRecipientChanged, dropped["k-"+signed], "a already signed; b still owes")
	assert.Equal(t, []string{arChannelMail, expiryChannelApp}, req.Channels)
	assert.Equal(t, live, req.ResourceID)
	assert.Equal(t, "/purchases/purchase_order/"+live, req.Link[len(req.Link)-len("/purchases/purchase_order/"+live):])
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "a@x.test", req.Recipients[0].Email)
	assert.NotEmpty(t, req.Recipients[0].UserID)
	assert.Equal(t, approvalEventType, req.EventType)
}
