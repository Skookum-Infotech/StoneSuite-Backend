//go:build dbtest

package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrainTimeoutIsUnknownAndNotRetried(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"deadline", fmt.Errorf("send: %w", context.DeadlineExceeded)},
		{"net timeout", fmt.Errorf("send: %w", timeoutNetErr{timeout: true})},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newDrainFx(t)
			id := f.insert(dRow{})
			s := &fakeSender{err: tc.err}
			res, err := f.drain(drainEnv(), s, fakeRenderer{})
			require.NoError(t, err)
			assert.Zero(t, res.Sent)
			assert.Zero(t, res.Failed)
			st := f.state(id)
			assert.Equal(t, "unknown", st.Status)
			assert.Nil(t, st.Lease)
			assert.NotEmpty(t, st.LastErr)

			later := drainEnv()
			later.Now = later.Now.Add(3 * time.Hour)
			s.err = nil
			_, err = f.drain(later, s, fakeRenderer{})
			require.NoError(t, err)
			assert.Len(t, s.reqs, 1, "unknown is never retried automatically")
		})
	}
}

func TestDrainTransientFailureBacksOff(t *testing.T) {
	f := newDrainFx(t)
	id := f.insert(dRow{})
	s := &fakeSender{err: errors.New(strings.Repeat("x", 800))}
	env := drainEnv()

	res, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Failed)
	st := f.state(id)
	assert.Equal(t, "failed", st.Status)
	assert.Equal(t, 1, st.Attempts)
	assert.True(t, st.RunAt.Equal(env.Now.Add(5*time.Minute)), "run_at %v", st.RunAt)
	assert.Len(t, st.LastErr, 500)

	// Not due yet: nothing happens.
	_, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Len(t, s.reqs, 1)

	// Due again: second failure backs off 30m.
	env.Now = env.Now.Add(6 * time.Minute)
	_, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	st = f.state(id)
	assert.Equal(t, 2, st.Attempts)
	assert.True(t, st.RunAt.Equal(env.Now.Add(30*time.Minute)))
}

func TestDrainFifthFailureIsDead(t *testing.T) {
	f := newDrainFx(t)
	id := f.insert(dRow{status: "failed", attempt: 4})
	s := &fakeSender{err: errors.New("boom")}
	res, err := f.drain(drainEnv(), s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Failed)
	st := f.state(id)
	assert.Equal(t, "dead", st.Status)
	assert.Equal(t, 5, st.Attempts)

	later := drainEnv()
	later.Now = later.Now.Add(5 * time.Hour)
	_, err = f.drain(later, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Len(t, s.reqs, 1, "dead rows are never claimed")
}

func TestDrainReclaimUsesSameIdempotencyKey(t *testing.T) {
	f := newDrainFx(t)
	a := f.insert(dRow{stage: 1})
	b := f.insert(dRow{stage: 1})
	s := &fakeSender{}
	env := drainEnv()
	_, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	require.Len(t, s.reqs, 1)

	// Simulate a crash after the send but before it was recorded: rows still
	// claimed with an expired lease.
	expired := env.Now.Add(-time.Minute)
	_, err = f.pool.Exec(f.ctx, `UPDATE automation_deliveries SET status = 'claimed', sent_at = NULL, lease_until = $2
WHERE id = ANY($1::uuid[])`, []string{a, b}, expired)
	require.NoError(t, err)

	later := env
	later.Now = env.Now.Add(time.Minute)
	_, err = f.drain(later, s, fakeRenderer{})
	require.NoError(t, err)
	require.Len(t, s.reqs, 2)
	assert.Equal(t, s.reqs[0].IdempotencyKey, s.reqs[1].IdempotencyKey)
	assert.Equal(t, "sent", f.state(a).Status)
}

func TestDrainLiveLeaseNotReclaimed(t *testing.T) {
	f := newDrainFx(t)
	lease := drainEnv().Now.Add(time.Minute)
	id := f.insert(dRow{status: "claimed", lease: &lease})
	s := &fakeSender{}
	_, err := f.drain(drainEnv(), s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	assert.Equal(t, "claimed", f.state(id).Status)
}

func TestDrainPostSendWritesSurviveCancelledContext(t *testing.T) {
	f := newDrainFx(t)
	id := f.insert(dRow{})
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	s := &fakeSender{
		err:    context.DeadlineExceeded,
		onSend: func(context.Context) { cancel() }, // the tick deadline fires mid-send
	}
	res, err := drain(ctx, f.pool, drainEnv(), s, fakeRenderer{}, f.ruleID)
	require.NoError(t, err)
	assert.Zero(t, res.Sent)
	st := f.state(id)
	assert.Equal(t, "unknown", st.Status, "state write must outlive the cancelled tick context")
	assert.Nil(t, st.Lease)

	later := drainEnv()
	later.Now = later.Now.Add(3 * time.Hour)
	_, err = f.drain(later, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Len(t, s.reqs, 1, "unknown row must not be resent")
}

func TestDrainSuccessRecordedAfterCancelledContext(t *testing.T) {
	f := newDrainFx(t)
	id := f.insert(dRow{})
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	s := &fakeSender{onSend: func(context.Context) { cancel() }}
	res, err := drain(ctx, f.pool, drainEnv(), s, fakeRenderer{}, f.ruleID)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Sent)
	assert.Equal(t, "sent", f.state(id).Status)
}

func TestDrainReleasesGroupsWhenOutOfTime(t *testing.T) {
	f := newDrainFx(t)
	a := f.insert(dRow{digest: "d-a", attempt: 1, status: "failed"})
	b := f.insert(dRow{digest: "d-b"})
	ctx, cancel := context.WithTimeout(f.ctx, minGroupBudget-10*time.Second)
	defer cancel()
	s := &fakeSender{}
	res, err := drain(ctx, f.pool, drainEnv(), s, fakeRenderer{}, f.ruleID)
	require.NoError(t, err)
	assert.Equal(t, Result{}, res)
	assert.Empty(t, s.reqs)
	for _, id := range []string{a, b} {
		st := f.state(id)
		assert.Equal(t, "planned", st.Status)
		assert.Nil(t, st.Lease)
	}
	assert.Equal(t, 1, f.state(a).Attempts, "attempts unchanged")
}
