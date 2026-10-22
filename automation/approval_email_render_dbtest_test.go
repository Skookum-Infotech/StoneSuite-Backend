//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/tenancy"
)

const renderFrontend = "https://app.example.test"

// vendorBill inserts a vendor bill at PAPV owned by emp with an entry history row at the given UTC wall clock.
func (f *arFixture) vendorBill(emp int, entered time.Time) string {
	var vendorID, id int
	var uuid string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_legal_name) VALUES ($1, $2, 'Acme Supply') RETURNING vendor_id`,
		f.typeID("VNDR"), f.statusID("VNDR", "ACT_")).Scan(&vendorID))
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO vendor_bill (record_type, vendor_bill_status, vendor_bill_approval_status, vendor_bill_vendor_id,
			vendor_bill_owner_id, vendor_bill_number)
		VALUES ($1, $2, 'pending', $3, $4, 'VBIL-' || substr(gen_random_uuid()::text, 1, 8))
		RETURNING vendor_bill_uuid::text, vendor_bill_id`,
		f.typeID("VBIL"), f.statusID("VBIL", "PAPV"), vendorID, emp).Scan(&uuid, &id))
	_, err := f.tx.Exec(f.ctx, `INSERT INTO vendor_bill_history (vendor_bill_id, to_status_id, at) VALUES ($1, $2, $3)`,
		id, f.statusID("VBIL", "PAPV"), entered)
	require.NoError(f.t, err)
	_, err = f.tx.Exec(f.ctx, `INSERT INTO vendor_bill_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, f.typeID("VBIL"), f.statusID("VBIL", "PAPV"), emp)
	require.NoError(f.t, err)
	return uuid
}

func renderEnv(loc *time.Location, today time.Time) JobEnv {
	return JobEnv{Tenant: &tenancy.Tenant{ID: "t1"}, Today: today, Loc: loc}
}

func delivery(resource, uuid string, anchor time.Time) Delivery {
	return Delivery{Resource: resource, RecordID: uuid, OccurrenceKey: "k-" + resource + "-" + uuid,
		RecipientEmail: "a@x.test", RecipientKind: recipientInternal, Anchor: anchor}
}

func setFrontend(t *testing.T) {
	old := config.AppConfig.FrontendURL
	config.AppConfig.FrontendURL = renderFrontend
	t.Cleanup(func() { config.AppConfig.FrontendURL = old })
}

func TestApprovalRenderOldestAndCounts(t *testing.T) {
	setFrontend(t)
	f := newARFixture(t)
	a, _ := f.employeeWithUser("a@x.test", "active")
	f.approver(a)
	newer, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	older, _ := f.po(apprPO{owner: &a, entered: atDays(6, 12)})
	gone, goneID := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_deleted_at = now(), purchase_order_deleted_by = 1 WHERE purchase_order_id = $1`, goneID)
	require.NoError(t, err)
	moved, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})

	g := Group{DigestKey: "d", RecipientEmail: "a@x.test", Items: []Delivery{
		delivery("purchase_order", newer, atDays(3, 0)), delivery("purchase_order", older, atDays(6, 0)),
		delivery("purchase_order", gone, atDays(3, 0)), delivery("purchase_order", moved, atDays(5, 0))}}
	req, kept, dropped, err := approvalRenderer{q: f.tx}.Render(f.ctx, renderEnv(time.UTC, arToday), g)
	require.NoError(t, err)

	require.Len(t, kept, 2)
	assert.Equal(t, newer, kept[0].RecordID) // input order preserved
	assert.Equal(t, older, req.ResourceID)
	assert.Equal(t, "/purchases/purchase_order/"+older, req.Link)
	assert.Equal(t, renderFrontend+"/purchases/purchase_order/"+older, req.Email.ActionURL)
	assert.Equal(t, "Approval reminder", req.Title)
	assert.Equal(t, "2 records are waiting for your approval.", req.Body)
	assert.Equal(t, reasonVoided, dropped["k-purchase_order-"+gone])
	assert.Equal(t, reasonAnchorChanged, dropped["k-purchase_order-"+moved])
	require.Len(t, req.Email.Details, 2)
	assert.Equal(t, "waiting 6 days", req.Email.Details[0].Value)
	assert.Equal(t, "waiting 3 days", req.Email.Details[1].Value)
}

func TestApprovalRenderTenantLocalDays(t *testing.T) {
	setFrontend(t)
	f := newARFixture(t)
	a, _ := f.employeeWithUser("a@x.test", "active")
	f.approver(a)
	chi, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	// 02:30Z Oct 13 is Oct 12 local, so on Oct 16 local it has waited 4 days (3 by the UTC date).
	uuid, _ := f.po(apprPO{owner: &a, entered: time.Date(2026, 10, 13, 2, 30, 0, 0, time.UTC)})
	g := Group{DigestKey: "d", RecipientEmail: "a@x.test", Items: []Delivery{
		delivery("purchase_order", uuid, time.Date(2026, 10, 12, 0, 0, 0, 0, chi))}}
	req, kept, dropped, err := approvalRenderer{q: f.tx}.Render(f.ctx, renderEnv(chi, time.Date(2026, 10, 16, 0, 0, 0, 0, chi)), g)
	require.NoError(t, err)
	require.Len(t, kept, 1, "dropped: %v", dropped)
	assert.Equal(t, "waiting 4 days", req.Email.Details[0].Value)
	assert.Equal(t, "1 record is waiting for your approval.", req.Body)
}

func TestApprovalRenderTwoWorkflows(t *testing.T) {
	setFrontend(t)
	f := newARFixture(t)
	a, _ := f.employeeWithUser("a@x.test", "active")
	f.approver(a)
	po, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	vb := f.vendorBill(a, atDays(5, 12))
	g := Group{DigestKey: "d", RecipientEmail: "a@x.test", Items: []Delivery{
		delivery("purchase_order", po, atDays(3, 0)), delivery("vendor_bill", vb, atDays(5, 0))}}
	req, kept, dropped, err := approvalRenderer{q: f.tx}.Render(f.ctx, renderEnv(time.UTC, arToday), g)
	require.NoError(t, err)
	require.Len(t, kept, 2, "dropped: %v", dropped)
	require.Len(t, req.Email.Details, 2)
	assert.Equal(t, "Vendor Bill", req.Email.Details[0].Label[:len("Vendor Bill")])
	assert.Equal(t, "Purchase Order", req.Email.Details[1].Label[:len("Purchase Order")])
	assert.Equal(t, "vendor_bill", req.Resource)
	assert.Equal(t, "/purchases/vendor_bill/"+vb, req.Link)
}

func TestApprovalRenderSupersedesDuplicateRecord(t *testing.T) {
	f := newARFixture(t)
	a, _ := f.employeeWithUser("a@x.test", "active")
	f.approver(a)
	po, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	first, second := delivery("purchase_order", po, atDays(3, 0)), delivery("purchase_order", po, atDays(3, 0))
	first.OccurrenceKey, second.OccurrenceKey = "gate1", "gate2"
	req, kept, dropped, err := approvalRenderer{q: f.tx}.Render(f.ctx, renderEnv(time.UTC, arToday),
		Group{DigestKey: "d", RecipientEmail: "a@x.test", Items: []Delivery{first, second}})
	require.NoError(t, err)
	require.Len(t, kept, 1)
	assert.Equal(t, "gate1", kept[0].OccurrenceKey)
	assert.Equal(t, map[string]string{"gate2": reasonSuperseded}, dropped)
	assert.Len(t, req.Email.Details, 1)
	assert.Equal(t, "1 record is waiting for your approval.", req.Body)
}

func TestApprovalRenderFallback(t *testing.T) {
	f := newARFixture(t)
	a, _ := f.employeeWithUser("a@x.test", "active")
	owner, _ := f.employeeWithUser("own@x.test", "active")
	f.approver(a)
	uuid, id := f.po(apprPO{owner: &owner, entered: atDays(3, 12)})
	f.signOff(id, a) // every live approver signed: escalate to the owner
	d := delivery("purchase_order", uuid, atDays(3, 0))
	d.RecipientEmail = "own@x.test"
	req, kept, dropped, err := approvalRenderer{q: f.tx}.Render(f.ctx, renderEnv(time.UTC, arToday),
		Group{DigestKey: "d", RecipientEmail: "own@x.test", Items: []Delivery{d}})
	require.NoError(t, err)
	require.Len(t, kept, 1, "dropped: %v", dropped)
	assert.Equal(t, approvalEmailReasonFallback, req.Email.Reason)
	require.Len(t, req.Email.Paragraphs, 2)
}

func TestApprovalRenderUnknownRecipientDropsAll(t *testing.T) {
	f := newARFixture(t)
	g := Group{DigestKey: "d", RecipientEmail: "nobody@x.test", Items: []Delivery{
		delivery("purchase_order", "00000000-0000-0000-0000-000000000000", arToday)}}
	_, kept, dropped, err := approvalRenderer{q: f.tx}.Render(f.ctx, renderEnv(time.UTC, arToday), g)
	require.NoError(t, err)
	assert.Empty(t, kept)
	for _, r := range dropped {
		assert.Equal(t, reasonRecipientChanged, r)
	}
	assert.Len(t, dropped, 1)
}
