//go:build dbtest

package automation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// poSpec returns the purchase_order spec at its PAPV gate.
func poSpec(t *testing.T) approvalSpec {
	t.Helper()
	for _, s := range approvalSpecs() {
		if s.Workflow == "purchase_order" {
			return s
		}
	}
	require.FailNow(t, "purchase_order spec missing")
	return approvalSpec{}
}

type apprPO struct {
	owner    *int
	status   string // lkp status code, default PAPV
	approval string // approval status column, default "pending"
	entered  time.Time
	deleted  bool
}

// po inserts a purchase order with a history row entering its status at p.entered (UTC wall clock).
func (f *arFixture) po(p apprPO) (uuid string, id int) {
	if p.status == "" {
		p.status = "PAPV"
	}
	if p.approval == "" {
		p.approval = "pending"
	}
	if p.approval == "rejected" {
		// Only in-place-reject modules allow 'rejected'; relax the check inside this rolled-back tx.
		_, err := f.tx.Exec(f.ctx, `ALTER TABLE purchase_order DROP CONSTRAINT IF EXISTS chk_po_approval_status`)
		require.NoError(f.t, err)
	}
	var vendorID int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_legal_name) VALUES ($1, $2, 'Acme Supply') RETURNING vendor_id`,
		f.typeID("VNDR"), f.statusID("VNDR", "ACT_")).Scan(&vendorID))
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO purchase_order (record_type, purchase_order_status, purchase_order_approval_status,
			purchase_order_vendor_id, purchase_order_owner_id, purchase_order_number)
		VALUES ($1, $2, $3, $4, $5, 'PORD-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING purchase_order_uuid::text, purchase_order_id`,
		f.typeID("PORD"), f.statusID("PORD", p.status), p.approval, vendorID, p.owner).Scan(&uuid, &id))
	f.enter(id, p.status, p.entered)
	if p.deleted {
		_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_deleted_at = now(), purchase_order_deleted_by = 1 WHERE purchase_order_id = $1`, id)
		require.NoError(f.t, err)
	}
	return uuid, id
}

// enter records a history row moving PO id into status at the given UTC wall-clock time.
func (f *arFixture) enter(id int, status string, at time.Time) {
	_, err := f.tx.Exec(f.ctx, `
		INSERT INTO purchase_order_history (purchase_order_id, to_status_id, at) VALUES ($1, $2, $3)`,
		id, f.statusID("PORD", status), at)
	require.NoError(f.t, err)
}

// approver configures employee as an active PORD/PAPV approver.
func (f *arFixture) approver(emp int) {
	_, err := f.tx.Exec(f.ctx, `
		INSERT INTO purchase_order_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, f.typeID("PORD"), f.statusID("PORD", "PAPV"), emp)
	require.NoError(f.t, err)
}

func (f *arFixture) signOff(poID, emp int) {
	_, err := f.tx.Exec(f.ctx, `
		INSERT INTO purchase_order_approval (purchase_order_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)`, poID, f.statusID("PORD", "PAPV"), emp)
	require.NoError(f.t, err)
}

// superAdmin creates an active user holding the super_admin role; returns the user id.
func (f *arFixture) superAdmin(email, status string) string {
	_, err := f.tx.Exec(f.ctx, `INSERT INTO roles (key, name, is_system) SELECT 'super_admin', 'Super Admin', TRUE
		WHERE NOT EXISTS (SELECT 1 FROM roles WHERE LOWER(key) = 'super_admin')`)
	require.NoError(f.t, err)
	return f.grantSuperAdmin(f.userOnly(email, status))
}

func (f *arFixture) userOnly(email, status string) string {
	var id string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO users (identity_id, email, status) VALUES (gen_random_uuid(), $1, $2) RETURNING id::text`,
		email, status).Scan(&id))
	return id
}

func (f *arFixture) grantSuperAdmin(userID string) string {
	_, err := f.tx.Exec(f.ctx, `INSERT INTO user_roles (user_id, role_id)
		SELECT $1::uuid, id FROM roles WHERE LOWER(key) = 'super_admin'`, userID)
	require.NoError(f.t, err)
	return userID
}

func atDays(n int, hour int) time.Time {
	return arToday.AddDate(0, 0, -n).Add(time.Duration(hour) * time.Hour)
}

func uuids(recs []ApprovalRecord) []string {
	var out []string
	for _, r := range recs {
		out = append(out, r.UUID)
	}
	return out
}

func userIDs(rs []approvalRecipient) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.UserID)
	}
	return out
}

func TestApprovalListCandidates(t *testing.T) {
	spec := poSpec(t)
	t.Run("age window and local date", func(t *testing.T) {
		f := newARFixture(t)
		emp, _ := f.employeeWithUser("own@x.test", "active")
		f.approver(emp)
		good, _ := f.po(apprPO{owner: &emp, entered: atDays(3, 12)})
		young, _ := f.po(apprPO{owner: &emp, entered: atDays(1, 12)})
		old, _ := f.po(apprPO{owner: &emp, entered: atDays(9, 12)})
		edgeLo, _ := f.po(apprPO{owner: &emp, entered: atDays(2, 0)})
		edgeHi, _ := f.po(apprPO{owner: &emp, entered: atDays(8, 23)})

		got, err := listApprovalCandidates(f.ctx, f.tx, spec, arToday, time.UTC)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{good, edgeLo, edgeHi}, uuids(got))
		assert.NotContains(t, uuids(got), young)
		assert.NotContains(t, uuids(got), old)
		for _, r := range got {
			if r.UUID == good {
				assert.True(t, r.Entered.Equal(arToday.AddDate(0, 0, -3)), "Entered is the local date at midnight")
				assert.Equal(t, "own@x.test", r.OwnerEmail)
				assert.Contains(t, r.Number, "PORD-")
				assert.Equal(t, emp, r.OwnerEmployeeID)
			}
		}
	})

	t.Run("anchor is the tenant-local date (Review Focus 1)", func(t *testing.T) {
		f := newARFixture(t)
		emp, _ := f.employeeWithUser("own@x.test", "active")
		f.approver(emp)
		chi, err := time.LoadLocation("America/Chicago")
		require.NoError(t, err)
		// 02:30 UTC on Mar 9 is 21:30 CDT on Mar 8.
		id, _ := f.po(apprPO{owner: &emp, entered: time.Date(2026, 3, 9, 2, 30, 0, 0, time.UTC)})
		today := time.Date(2026, 3, 10, 0, 0, 0, 0, chi)
		got, err := listApprovalCandidates(f.ctx, f.tx, spec, today, chi)
		require.NoError(t, err)
		require.Equal(t, []string{id}, uuids(got))
		assert.True(t, got[0].Entered.Equal(time.Date(2026, 3, 8, 0, 0, 0, 0, chi)), "got %s", got[0].Entered)
	})

	t.Run("rejected, ungated, deleted, wrong status excluded (Review Focus 4)", func(t *testing.T) {
		f := newARFixture(t)
		emp, _ := f.employeeWithUser("own@x.test", "active")
		ok, _ := f.po(apprPO{owner: &emp, entered: atDays(3, 12)}) // no approver yet: ungated
		got, err := listApprovalCandidates(f.ctx, f.tx, spec, arToday, time.UTC)
		require.NoError(t, err)
		assert.Empty(t, got, "zero configured approvers is never reminded")

		f.approver(emp)
		rejected, _ := f.po(apprPO{owner: &emp, approval: "rejected", entered: atDays(3, 12)})
		deleted, _ := f.po(apprPO{owner: &emp, deleted: true, entered: atDays(3, 12)})
		draft, _ := f.po(apprPO{owner: &emp, status: "DRFT", entered: atDays(3, 12)})
		got, err = listApprovalCandidates(f.ctx, f.tx, spec, arToday, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, []string{ok}, uuids(got))
		for _, id := range []string{rejected, deleted, draft} {
			assert.NotContains(t, uuids(got), id)
		}
	})

	t.Run("latest entry wins and inactive owner has no email", func(t *testing.T) {
		f := newARFixture(t)
		emp, _ := f.employeeWithUser("own@x.test", "active")
		off, _ := f.employeeWithUser("off@x.test", "disabled")
		f.approver(emp)
		reentered, id := f.po(apprPO{owner: &off, entered: atDays(7, 12)})
		f.enter(id, "PAPV", atDays(3, 12))
		got, err := listApprovalCandidates(f.ctx, f.tx, spec, arToday, time.UTC)
		require.NoError(t, err)
		require.Equal(t, []string{reentered}, uuids(got))
		assert.True(t, got[0].Entered.Equal(arToday.AddDate(0, 0, -3)))
		assert.Empty(t, got[0].OwnerEmail)
		assert.Equal(t, off, got[0].OwnerEmployeeID)
	})
}

func TestApprovalRecipients(t *testing.T) {
	spec := poSpec(t)
	build := func(f *arFixture, owner *int) ApprovalRecord {
		uuid, id := f.po(apprPO{owner: owner, entered: atDays(3, 12)})
		return ApprovalRecord{UUID: uuid, InternalID: id, OwnerEmployeeID: func() int {
			if owner == nil {
				return 0
			}
			return *owner
		}()}
	}

	t.Run("two approvers one signed (Review Focus 2)", func(t *testing.T) {
		f := newARFixture(t)
		a, aUser := f.employeeWithUser("a@x.test", "active")
		b, bUser := f.employeeWithUser("b@x.test", "active")
		f.approver(a)
		f.approver(b)
		rec := build(f, &a)
		got, err := approvalRecipients(f.ctx, f.tx, spec, rec)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{aUser, bUser}, userIDs(got))

		f.signOff(rec.InternalID, a)
		got, err = approvalRecipients(f.ctx, f.tx, spec, rec)
		require.NoError(t, err)
		require.Equal(t, []string{bUser}, userIDs(got))
		assert.False(t, got[0].Fallback)
		assert.Equal(t, "b@x.test", got[0].Email)
	})

	t.Run("orphaned gate escalates to owner and super admins (Review Focus 3)", func(t *testing.T) {
		f := newARFixture(t)
		a, _ := f.employeeWithUser("a@x.test", "disabled")
		f.approver(a)
		owner, ownerUser := f.employeeWithUser("owner@x.test", "active")
		sa1 := f.superAdmin("sa1@x.test", "active")
		sa2 := f.superAdmin("sa2@x.test", "active")
		f.superAdmin("sa-off@x.test", "disabled")
		rec := build(f, &owner)
		got, err := approvalRecipients(f.ctx, f.tx, spec, rec)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{ownerUser, sa1, sa2}, userIDs(got))
		for _, r := range got {
			assert.True(t, r.Fallback)
		}

		// An owner who is also a super admin appears once.
		f.grantSuperAdmin(ownerUser)
		got, err = approvalRecipients(f.ctx, f.tx, spec, rec)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{ownerUser, sa1, sa2}, userIDs(got))
	})

	t.Run("orphaned with a disabled owner omits the owner", func(t *testing.T) {
		f := newARFixture(t)
		a, _ := f.employeeWithUser("a@x.test", "disabled")
		f.approver(a)
		owner, _ := f.employeeWithUser("owner@x.test", "disabled")
		sa := f.superAdmin("sa@x.test", "active")
		got, err := approvalRecipients(f.ctx, f.tx, spec, build(f, &owner))
		require.NoError(t, err)
		assert.Equal(t, []string{sa}, userIDs(got))
	})
}

func TestApprovalState(t *testing.T) {
	spec := poSpec(t)
	anchor := arToday.AddDate(0, 0, -3)
	setup := func(t *testing.T) (*arFixture, string, int, string, int) {
		f := newARFixture(t)
		a, aUser := f.employeeWithUser("a@x.test", "active")
		f.approver(a)
		uuid, id := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
		return f, uuid, id, aUser, a
	}

	t.Run("still eligible", func(t *testing.T) {
		f, uuid, _, user, _ := setup(t)
		reason, rec, rcpt, err := approvalState(f.ctx, f.tx, spec, uuid, anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Empty(t, reason)
		assert.Equal(t, uuid, rec.UUID)
		assert.Equal(t, user, rcpt.UserID)
		assert.Equal(t, "a@x.test", rcpt.Email)
	})
	t.Run("voided when soft-deleted or missing", func(t *testing.T) {
		f, uuid, id, user, _ := setup(t)
		_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_deleted_at = now(), purchase_order_deleted_by = 1 WHERE purchase_order_id = $1`, id)
		require.NoError(t, err)
		reason, _, _, err := approvalState(f.ctx, f.tx, spec, uuid, anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, reasonVoided, reason)
		reason, _, _, err = approvalState(f.ctx, f.tx, spec, "00000000-0000-0000-0000-000000000000", anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, reasonVoided, reason)
	})
	t.Run("not pending after advancing out of the gate", func(t *testing.T) {
		f, uuid, id, user, _ := setup(t)
		_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_status = $2 WHERE purchase_order_id = $1`,
			id, f.statusID("PORD", "APPV"))
		require.NoError(t, err)
		reason, _, _, err := approvalState(f.ctx, f.tx, spec, uuid, anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, reasonNotPending, reason)
	})
	t.Run("not pending when rejected in place", func(t *testing.T) {
		f, uuid, id, user, _ := setup(t)
		_, err0 := f.tx.Exec(f.ctx, `ALTER TABLE purchase_order DROP CONSTRAINT IF EXISTS chk_po_approval_status`)
		require.NoError(t, err0)
		_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_approval_status = 'rejected' WHERE purchase_order_id = $1`, id)
		require.NoError(t, err)
		reason, _, _, err := approvalState(f.ctx, f.tx, spec, uuid, anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, reasonNotPending, reason)
	})
	t.Run("anchor changed when re-entered later", func(t *testing.T) {
		f, uuid, id, user, _ := setup(t)
		f.enter(id, "PAPV", atDays(1, 12))
		reason, rec, _, err := approvalState(f.ctx, f.tx, spec, uuid, anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, reasonAnchorChanged, reason)
		assert.True(t, rec.Entered.Equal(arToday.AddDate(0, 0, -1)))
	})
	t.Run("recipient changed once the approver signed", func(t *testing.T) {
		f, uuid, id, user, emp := setup(t)
		b, _ := f.employeeWithUser("b@x.test", "active")
		f.approver(b)
		f.signOff(id, emp)
		reason, _, _, err := approvalState(f.ctx, f.tx, spec, uuid, anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, reasonRecipientChanged, reason)
	})
	t.Run("precedence: voided before not_pending before anchor_changed", func(t *testing.T) {
		f, uuid, id, user, _ := setup(t)
		f.enter(id, "PAPV", atDays(1, 12))
		_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_status = $2 WHERE purchase_order_id = $1`,
			id, f.statusID("PORD", "APPV"))
		require.NoError(t, err)
		reason, _, _, err := approvalState(f.ctx, f.tx, spec, uuid, anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, reasonNotPending, reason, fmt.Sprintf("uuid %s", uuid))
	})
}
