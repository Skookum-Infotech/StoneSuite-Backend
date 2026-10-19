//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

	t.Run("self-transition history row does not restart the clock", func(t *testing.T) {
		f := newARFixture(t)
		emp, _ := f.employeeWithUser("own@x.test", "active")
		f.approver(emp)
		id, poID := f.po(apprPO{owner: &emp, entered: atDays(4, 12)})
		// A partial sign-off writes from = to = gate, later than entry.
		_, err := f.tx.Exec(f.ctx, `INSERT INTO purchase_order_history (purchase_order_id, from_status_id, to_status_id, action, at)
			VALUES ($1, $2, $2, 'approve', $3)`, poID, f.statusID("PORD", "PAPV"), atDays(1, 12))
		require.NoError(t, err)
		got, err := listApprovalCandidates(f.ctx, f.tx, spec, arToday, time.UTC)
		require.NoError(t, err)
		require.Equal(t, []string{id}, uuids(got))
		assert.True(t, got[0].Entered.Equal(arToday.AddDate(0, 0, -4)), "got %s", got[0].Entered)
		reason, rec, _, err := approvalState(f.ctx, f.tx, spec, id, arToday.AddDate(0, 0, -4), "", time.UTC)
		require.NoError(t, err)
		assert.NotEqual(t, reasonAnchorChanged, reason)
		assert.True(t, rec.Entered.Equal(arToday.AddDate(0, 0, -4)))
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

	t.Run("signed approver plus deactivated approver escalates", func(t *testing.T) {
		f := newARFixture(t)
		a, _ := f.employeeWithUser("a@x.test", "active")
		b, _ := f.employeeWithUser("b@x.test", "disabled")
		f.approver(a)
		f.approver(b)
		owner, ownerUser := f.employeeWithUser("owner@x.test", "active")
		sa := f.superAdmin("sa@x.test", "active")
		rec := build(f, &owner)
		f.signOff(rec.InternalID, a)
		got, err := approvalRecipients(f.ctx, f.tx, spec, rec)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{ownerUser, sa}, userIDs(got))
		for _, r := range got {
			assert.True(t, r.Fallback)
		}
	})

	t.Run("unsigned live approver present means no fallback", func(t *testing.T) {
		f := newARFixture(t)
		a, aUser := f.employeeWithUser("a@x.test", "active")
		b, _ := f.employeeWithUser("b@x.test", "disabled")
		f.approver(a)
		f.approver(b)
		f.superAdmin("sa@x.test", "active")
		got, err := approvalRecipients(f.ctx, f.tx, spec, build(f, &a))
		require.NoError(t, err)
		require.Equal(t, []string{aUser}, userIDs(got))
		assert.False(t, got[0].Fallback)
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
