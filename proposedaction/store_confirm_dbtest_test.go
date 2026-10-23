//go:build dbtest

package proposedaction

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/authz"
	"stonesuite-backend/provenance"
)

func TestConfirm_HappyPath(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	a, replayed, err := f.store.Confirm(f.ctx, f.caller, id)
	require.NoError(t, err)
	assert.False(t, replayed)
	assert.Equal(t, StatusConfirmed, a.Status)
	require.NotNil(t, a.ResultRef)
	assert.Equal(t, string(testResource), a.ResultRef.Resource)
	assert.EqualValues(t, 1, f.exec.calls.Load())
	assert.Equal(t, provenance.AIAgent, f.exec.via.Load())

	var via, evType string
	require.NoError(t, f.pool.QueryRow(f.ctx, `
		SELECT via, event_type FROM domain_event WHERE record_id = $1 AND event_type = $2`,
		a.ResultRef.RecordID, eventConfirmedType).Scan(&via, &evType))
	assert.Equal(t, string(provenance.AIAgent), via)
}

func TestConfirm_DocExtractVia(t *testing.T) {
	f := newFx(t)
	id, err := f.store.Propose(f.ctx, f.pool, Proposal{Caller: f.caller, Kind: KindCreate,
		Resource: string(testResource), Source: SourceDocExtract, Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	_, _, err = f.store.Confirm(f.ctx, f.caller, id)
	require.NoError(t, err)
	assert.Equal(t, provenance.DocExtract, f.exec.via.Load())
}

func TestConfirm_NotFound(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	other := Caller{IdentityID: "22222222-2222-2222-2222-222222222222", UserID: f.caller.UserID}

	_, _, err := f.store.Confirm(f.ctx, other, id)
	var nf *NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.True(t, nf.Mismatch)

	_, _, err = f.store.Confirm(f.ctx, f.caller, "33333333-3333-3333-3333-333333333333")
	require.ErrorAs(t, err, &nf)
	assert.False(t, nf.Mismatch)

	_, _, err = f.store.Confirm(f.ctx, f.caller, "not-a-uuid")
	require.ErrorAs(t, err, &nf)
	assert.False(t, nf.Mismatch)
	assert.Zero(t, f.exec.calls.Load())
}

func TestConfirm_Expired(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	_, err := f.pool.Exec(f.ctx, `UPDATE ai_proposed_action SET expires_at = now() - interval '1 minute' WHERE id = $1`, id)
	require.NoError(t, err)
	_, _, err = f.store.Confirm(f.ctx, f.caller, id)
	assert.ErrorIs(t, err, ErrExpired)
	assert.Equal(t, StatusExpired, f.status(id))
	assert.Zero(t, f.exec.calls.Load())
}

func TestConfirm_ReplayRunsOnce(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	first, _, err := f.store.Confirm(f.ctx, f.caller, id)
	require.NoError(t, err)
	second, replayed, err := f.store.Confirm(f.ctx, f.caller, id)
	require.NoError(t, err)
	assert.True(t, replayed)
	assert.Equal(t, first.ResultRef, second.ResultRef)
	assert.EqualValues(t, 1, f.exec.calls.Load())
}

func TestConfirm_CancelledIsConflict(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	_, err := f.store.Cancel(f.ctx, f.caller.IdentityID, id)
	require.NoError(t, err)
	_, _, err = f.store.Confirm(f.ctx, f.caller, id)
	assert.ErrorIs(t, err, ErrConflict)
}

func TestConfirm_RoleRevokedAfterProposal(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	require.NoError(t, authz.UnassignRole(f.ctx, f.pool, f.caller.UserID, f.roleID))
	_, _, err := f.store.Confirm(f.ctx, f.caller, id)
	var fe *ForbiddenError
	require.ErrorAs(t, err, &fe)
	assert.Equal(t, testResource, fe.Resource)
	assert.Zero(t, f.exec.calls.Load())
	assert.Equal(t, StatusPending, f.status(id))
}

func TestConfirm_OutOfScopeIsNotFound(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	f.exec.denyScope = true
	_, _, err := f.store.Confirm(f.ctx, f.caller, id)
	var nf *NotFoundError
	require.ErrorAs(t, err, &nf)
	assert.True(t, nf.Mismatch, "scope denial must be logged as an IDOR attempt")
	assert.NotErrorIs(t, err, ErrForbidden)
	assert.Zero(t, f.exec.calls.Load())
	assert.Equal(t, StatusPending, f.status(id))
}

func TestPropose_RejectsInvalid(t *testing.T) {
	f := newFx(t)
	tests := []struct {
		name string
		p    Proposal
	}{
		{"bad payload", Proposal{Caller: f.caller, Kind: KindCreate, Resource: string(testResource), Source: SourceAgent, Payload: json.RawMessage(`[1]`)}},
		{"unknown resource", Proposal{Caller: f.caller, Kind: KindCreate, Resource: "nope", Source: SourceAgent, Payload: json.RawMessage(`{}`)}},
		{"bad kind", Proposal{Caller: f.caller, Kind: Kind("x"), Resource: string(testResource), Source: SourceAgent, Payload: json.RawMessage(`{}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.store.Propose(f.ctx, f.pool, tt.p)
			assert.ErrorIs(t, err, ErrInvalidPayload)
		})
	}
}

func TestConfirm_PayloadBecomesInvalid(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	f.exec.failValid.Store(true)
	_, _, err := f.store.Confirm(f.ctx, f.caller, id)
	assert.ErrorIs(t, err, ErrInvalidPayload)
	assert.Equal(t, StatusFailed, f.status(id))
	assert.Zero(t, f.exec.calls.Load())
	a, err := f.store.Get(f.ctx, f.caller.IdentityID, id)
	require.NoError(t, err)
	assert.Equal(t, msgNoLongerValid, a.ErrorMessage)
}

func TestConfirm_ExecutorErrorRollsBack(t *testing.T) {
	f := newFx(t)
	f.exec.markerKey = "pa_marker_" + f.caller.UserID
	f.exec.execErr = assert.AnError
	id := f.propose()
	_, _, err := f.store.Confirm(f.ctx, f.caller, id)
	assert.ErrorIs(t, err, ErrExecutionFailed)

	var n int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM roles WHERE key = $1`, f.exec.markerKey).Scan(&n))
	assert.Zero(t, n, "executor writes must roll back")
	a, err := f.store.Get(f.ctx, f.caller.IdentityID, id)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, a.Status)
	assert.Equal(t, msgNotCompleted, a.ErrorMessage)
	assert.NotContains(t, a.ErrorMessage, assert.AnError.Error())
}

func TestConfirm_ConcurrentExecutesOnce(t *testing.T) {
	f := newFx(t)
	id := f.propose()
	const workers = 6
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = f.store.Confirm(f.ctx, f.caller, id)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		assert.NoError(t, err)
	}
	assert.EqualValues(t, 1, f.exec.calls.Load())
}
