//go:build dbtest

package proposedaction

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestList_ScopedAndPaged(t *testing.T) {
	f := newFx(t)
	other := newFx(t)
	var ids []string
	for i := 0; i < 5; i++ {
		ids = append(ids, f.propose())
	}
	other.propose()

	page1, next, err := f.store.List(f.ctx, f.caller.IdentityID, 2, "")
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.NotEmpty(t, next)
	page2, next, err := f.store.List(f.ctx, f.caller.IdentityID, 2, next)
	require.NoError(t, err)
	require.Len(t, page2, 2)
	page3, next, err := f.store.List(f.ctx, f.caller.IdentityID, 2, next)
	require.NoError(t, err)
	require.Len(t, page3, 1)
	assert.Empty(t, next)

	seen := map[string]bool{}
	for _, a := range append(append(page1, page2...), page3...) {
		assert.Equal(t, f.caller.IdentityID, a.IdentityID)
		seen[a.ID] = true
	}
	assert.Len(t, seen, 5)
	for _, id := range ids {
		assert.True(t, seen[id])
	}

	_, _, err = f.store.List(f.ctx, f.caller.IdentityID, 2, "garbage!")
	assert.ErrorIs(t, err, ErrInvalidCursor)
}

func TestList_ExcludesNonPendingAndExpired(t *testing.T) {
	f := newFx(t)
	keep, gone, expired := f.propose(), f.propose(), f.propose()
	_, err := f.store.Cancel(f.ctx, f.caller.IdentityID, gone)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET expires_at = now() - interval '1 minute' WHERE id = $1`, expired)
	require.NoError(t, err)
	got, _, err := f.store.List(f.ctx, f.caller.IdentityID, 0, "")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, keep, got[0].ID)
}

func TestGet_OwnershipAndLazyExpiry(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	_, err := f.store.Get(f.ctx, "22222222-2222-2222-2222-222222222222", id)
	var nf *NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.True(t, nf.Mismatch)
	_, err = f.store.Get(f.ctx, f.caller.IdentityID, "bad")
	require.ErrorAs(t, err, &nf)
	assert.False(t, nf.Mismatch)

	_, err = f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET expires_at = now() - interval '1 minute' WHERE id = $1`, id)
	require.NoError(t, err)
	a, err := f.store.Get(f.ctx, f.caller.IdentityID, id)
	require.NoError(t, err)
	assert.Equal(t, StatusExpired, a.Status)
}

func TestCancel(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	a, err := f.store.Cancel(f.ctx, f.caller.IdentityID, id)
	require.NoError(t, err)
	assert.Equal(t, StatusCancelled, a.Status)
	again, err := f.store.Cancel(f.ctx, f.caller.IdentityID, id)
	require.NoError(t, err)
	assert.Equal(t, StatusCancelled, again.Status)

	_, err = f.store.Cancel(f.ctx, "22222222-2222-2222-2222-222222222222", f.propose())
	assert.ErrorIs(t, err, ErrNotFound)

	confirmed := f.propose()
	_, _, err = f.store.Confirm(f.ctx, f.caller, confirmed)
	require.NoError(t, err)
	_, err = f.store.Cancel(f.ctx, f.caller.IdentityID, confirmed)
	assert.ErrorIs(t, err, ErrConflict)

	late := f.propose()
	_, err = f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET expires_at = now() - interval '1 minute' WHERE id = $1`, late)
	require.NoError(t, err)
	_, err = f.store.Cancel(f.ctx, f.caller.IdentityID, late)
	assert.ErrorIs(t, err, ErrExpired)
}

func TestSweep(t *testing.T) {
	f := newFx(t)
	overdue, old, fresh := f.propose(), f.propose(), f.propose()
	_, err := f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET expires_at = now() - interval '1 minute' WHERE id = $1`, overdue)
	require.NoError(t, err)
	_, err = f.store.Cancel(f.ctx, f.caller.IdentityID, old)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET resolved_at = now() - interval '30 days' WHERE id = $1`, old)
	require.NoError(t, err)

	expired, deleted, err := f.store.Sweep(f.ctx, f.pool, 7)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, expired, int64(1))
	assert.GreaterOrEqual(t, deleted, int64(1))
	assert.Equal(t, StatusExpired, f.status(overdue))
	assert.Equal(t, StatusPending, f.status(fresh))
	var n int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM ai_proposed_action WHERE id = $1`, old).Scan(&n))
	assert.Zero(t, n)
}
