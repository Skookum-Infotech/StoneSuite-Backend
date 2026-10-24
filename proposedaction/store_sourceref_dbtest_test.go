//go:build dbtest

package proposedaction

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// proposeRef inserts a docextract proposal carrying the given sourceRef.
func (f *fx) proposeRef(ref string) string {
	f.t.Helper()
	id, err := f.store.Propose(f.ctx, f.pool, Proposal{
		Caller: f.caller, Kind: KindCreate, Resource: string(testResource),
		Source: SourceDocExtract, Payload: json.RawMessage(fmt.Sprintf(`{"sourceRef":%q}`, ref)),
	})
	require.NoError(f.t, err)
	return id
}

func (f *fx) setStatus(id, status string) {
	f.t.Helper()
	_, err := f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET status = $2 WHERE id = $1`, id, status)
	require.NoError(f.t, err)
}

func (f *fx) expire(id string) {
	f.t.Helper()
	_, err := f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET expires_at = now() - interval '1 minute' WHERE id = $1`, id)
	require.NoError(f.t, err)
}

func TestListBySourceRef(t *testing.T) {
	f := newFx(t)
	other := newFx(t)
	a := f.proposeRef("ext:1:customer")
	b := f.proposeRef("ext:1:item:0")
	f.proposeRef("ext:2:customer")
	other.proposeRef("ext:1:customer")

	got, err := f.store.ListBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, "ext:1:")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, b, got[0].ID, "newest first")
	assert.Equal(t, a, got[1].ID)

	lit, err := f.store.ListBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, "ext:%")
	require.NoError(t, err)
	assert.Empty(t, lit, "LIKE metacharacters are literal")

	wrong, err := f.store.ListBySourceRef(f.ctx, f.caller.IdentityID, SourceAgent, "ext:1:")
	require.NoError(t, err)
	assert.Empty(t, wrong)

	f.expire(a)
	got, err = f.store.ListBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, "ext:1:customer")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, StatusExpired, got[0].Status, "reported expired")
	assert.Equal(t, StatusPending, f.status(a), "not mutated")
}

func TestFindActiveBySourceRef(t *testing.T) {
	f := newFx(t)
	other := newFx(t)
	const ref = "ext:9:customer"

	_, ok, err := f.store.FindActiveBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, ref)
	require.NoError(t, err)
	assert.False(t, ok)

	other.proposeRef(ref)
	_, ok, err = f.store.FindActiveBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, ref)
	require.NoError(t, err)
	assert.False(t, ok, "other identity's row is invisible")

	id := f.proposeRef(ref)
	a, ok, err := f.store.FindActiveBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, ref)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, id, a.ID)

	f.setStatus(id, "cancelled")
	_, ok, err = f.store.FindActiveBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, ref)
	require.NoError(t, err)
	assert.False(t, ok, "cancelled ignored")

	exp := f.proposeRef(ref)
	f.expire(exp)
	_, ok, err = f.store.FindActiveBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, ref)
	require.NoError(t, err)
	assert.False(t, ok, "expired ignored")

	conf := f.proposeRef(ref)
	f.setStatus(conf, "confirmed")
	a, ok, err = f.store.FindActiveBySourceRef(f.ctx, f.caller.IdentityID, SourceDocExtract, ref)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, conf, a.ID)
	assert.Equal(t, StatusConfirmed, a.Status)
}
