//go:build dbtest

package automation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	t.Run("deleted and advanced is voided", func(t *testing.T) {
		f, uuid, id, user, _ := setup(t)
		_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_status = $2,
			purchase_order_deleted_at = now(), purchase_order_deleted_by = 1 WHERE purchase_order_id = $1`,
			id, f.statusID("PORD", "APPV"))
		require.NoError(t, err)
		reason, _, _, err := approvalState(f.ctx, f.tx, spec, uuid, anchor, user, time.UTC)
		require.NoError(t, err)
		assert.Equal(t, reasonVoided, reason)
	})
	t.Run("not pending when the only approver config is deactivated", func(t *testing.T) {
		f, uuid, _, user, _ := setup(t)
		_, err := f.tx.Exec(f.ctx, `UPDATE purchase_order_approver SET is_active = FALSE`)
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
