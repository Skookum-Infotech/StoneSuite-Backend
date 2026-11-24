//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type vbRow struct {
	vendor  int
	status  string
	due     *time.Time
	total   string
	paid    string
	credit  string
	owner   *int
	deleted bool
}

func (f *arFixture) vendor() int {
	var id int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_legal_name) VALUES ($1, $2, 'Granite Co') RETURNING vendor_id`,
		f.typeID("VNDR"), f.statusID("VNDR", "ACT_")).Scan(&id))
	return id
}

func (f *arFixture) vbRowInsert(b vbRow) string {
	var uuid string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO vendor_bill (record_type, vendor_bill_status, vendor_bill_vendor_id, vendor_bill_vendor_name,
			vendor_bill_due_date, vendor_bill_grand_total, vendor_bill_amount_paid, vendor_bill_credit_total, vendor_bill_owner_id)
		VALUES ($1, $2, $3, 'Granite Co', $4, $5::numeric, $6::numeric, $7::numeric, $8) RETURNING vendor_bill_uuid::text`,
		f.typeID("VBIL"), f.statusID("VBIL", b.status), b.vendor, b.due, b.total, b.paid, b.credit, b.owner).Scan(&uuid))
	if b.deleted {
		_, err := f.tx.Exec(f.ctx, `UPDATE vendor_bill SET vendor_bill_deleted_at = now(), vendor_bill_deleted_by = 1 WHERE vendor_bill_uuid = $1::uuid`, uuid)
		require.NoError(f.t, err)
	}
	return uuid
}

func TestVendorBillListCandidates(t *testing.T) {
	f := newARFixture(t)
	vendor := f.vendor()
	owner, _ := f.employeeWithUser("vb-owner@x.io", "active")
	mk := func(b vbRow) string {
		b.vendor = vendor
		if b.total == "" {
			b.total = "1000"
		}
		if b.paid == "" {
			b.paid = "0"
		}
		if b.credit == "" {
			b.credit = "0"
		}
		return f.vbRowInsert(b)
	}
	approved := mk(vbRow{status: "APPV", due: dayOffset(3), owner: &owner})
	part := mk(vbRow{status: "PART", due: dayOffset(0), paid: "400"})
	overdue := mk(vbRow{status: "ODUE", due: dayOffset(-2)})
	mk(vbRow{status: "DRFT", due: dayOffset(3)})
	mk(vbRow{status: "PAPV", due: dayOffset(3)})
	mk(vbRow{status: "PAID", due: dayOffset(3), paid: "1000"})
	mk(vbRow{status: "VOID", due: dayOffset(3)})
	mk(vbRow{status: "PINI", due: dayOffset(3)})
	mk(vbRow{status: "APPV", due: dayOffset(3), credit: "1000"})
	mk(vbRow{status: "APPV", due: dayOffset(3), deleted: true})
	mk(vbRow{status: "APPV", due: dayOffset(20)})
	mk(vbRow{status: "APPV", due: dayOffset(-10)})
	mk(vbRow{status: "APPV", due: nil})

	got, err := listVendorBillCandidates(f.ctx, f.tx, arToday)
	require.NoError(t, err)
	var ids []string
	for _, d := range got {
		ids = append(ids, d.UUID)
	}
	assert.ElementsMatch(t, []string{approved, part, overdue}, ids,
		"only approved, partly paid and overdue bills with a balance, due in the window, qualify")

	byID := map[string]VendorBillDoc{}
	for _, d := range got {
		byID[d.UUID] = d
	}
	assert.InDelta(t, 1000, byID[approved].Balance, 0.001)
	assert.InDelta(t, 600, byID[part].Balance, 0.001, "balance is total minus paid minus credits")
	assert.Equal(t, "vb-owner@x.io", byID[approved].OwnerEmail)
	assert.Equal(t, "Granite Co", byID[approved].VendorName)
	assert.Empty(t, byID[part].OwnerEmail, "a bill with no owner has no recipient")
}

func TestVendorBillEligible(t *testing.T) {
	f := newARFixture(t)
	vendor := f.vendor()
	owner, _ := f.employeeWithUser("vb-live@x.io", "active")
	base := vbRow{vendor: vendor, status: "APPV", due: dayOffset(3), total: "500", paid: "0", credit: "0", owner: &owner}
	mut := func(fn func(*vbRow)) string { b := base; fn(&b); return f.vbRowInsert(b) }

	open := f.vbRowInsert(base)
	planned := *dayOffset(3)

	ok, reason, live, err := vendorBillEligible(f.ctx, f.tx, open, planned)
	require.NoError(t, err)
	assert.True(t, ok, reason)
	assert.Equal(t, "vb-live@x.io", live.OwnerEmail)

	tests := []struct {
		name string
		uuid string
		want string
	}{
		{"paid", mut(func(b *vbRow) { b.status, b.paid = "PAID", "500" }), reasonPaid},
		{"void", mut(func(b *vbRow) { b.status = "VOID" }), reasonVoided},
		{"deleted", mut(func(b *vbRow) { b.deleted = true }), reasonVoided},
		{"payment in flight", mut(func(b *vbRow) { b.status = "PINI" }), reasonPaymentInitiated},
		{"not approved", mut(func(b *vbRow) { b.status = "DRFT" }), reasonNotPayable},
		{"credit covers it", mut(func(b *vbRow) { b.credit = "500" }), reasonPaid},
		{"due date moved", mut(func(b *vbRow) { b.due = dayOffset(9) }), reasonAnchorChanged},
		{"no owner", mut(func(b *vbRow) { b.owner = nil }), reasonNoEmail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, reason, _, err := vendorBillEligible(f.ctx, f.tx, tc.uuid, planned)
			require.NoError(t, err)
			assert.False(t, ok)
			assert.Equal(t, tc.want, reason)
		})
	}

	ok, reason, _, err = vendorBillEligible(f.ctx, f.tx, "123e4567-e89b-12d3-a456-426614174999", planned)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, reasonVoided, reason, "a bill that no longer exists is dropped as voided")
}
