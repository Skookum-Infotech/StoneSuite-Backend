//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nextDayWindow is the send-window start of the local day after env.Today.
func nextDayWindow(env JobEnv) time.Time {
	return NextSendWindow(localDayStart(env.Today, env.Loc).AddDate(0, 0, 1), env.Loc).UTC()
}

func TestDrainDefersWhenDailyCapReached(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET daily_external_cap = 1`)
	require.NoError(t, err)
	a := f.insert(dRow{email: "a-" + f.email, attempt: 2})
	b := f.insert(dRow{email: "b-" + f.email, attempt: 2})
	s := &fakeSender{}
	res, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Len(t, s.reqs, 1)
	assert.Equal(t, 0, res.Skipped)
	deferred := a
	if f.state(a).Status == "sent" {
		deferred = b
	}
	st := f.state(deferred)
	assert.Equal(t, "planned", st.Status)
	assert.Equal(t, nextDayWindow(env), st.RunAt.UTC())
	assert.Nil(t, st.Lease)
	assert.Equal(t, 2, st.Attempts)

	next := env
	next.Today = env.Today.AddDate(0, 0, 1)
	next.Now = env.Now.AddDate(0, 0, 1)
	_, err = f.drain(next, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, "sent", f.state(deferred).Status)
}

func TestDrainDefersUnderKillSwitch(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	id := f.insert(dRow{attempt: 2})
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET kill_switch = TRUE`)
	require.NoError(t, err)
	s := &fakeSender{}
	res, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	assert.Equal(t, 0, res.Skipped)
	st := f.state(id)
	assert.Equal(t, "planned", st.Status)
	assert.Equal(t, NextSendWindow(env.Now, env.Loc).Add(time.Hour), st.RunAt.UTC())
	assert.Nil(t, st.Lease)
	assert.Equal(t, 2, st.Attempts)

	_, err = f.pool.Exec(f.ctx, `UPDATE automation_settings SET kill_switch = FALSE`)
	require.NoError(t, err)
	next := env
	next.Today = env.Today.AddDate(0, 0, 1)
	next.Now = env.Now.AddDate(0, 0, 1)
	_, err = f.drain(next, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, "sent", f.state(id).Status)
}

func TestDrainStaleAfterLongDeferral(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	// Created 49h ago and repeatedly deferred: run_at is in the future.
	id := f.insert(dRow{runAt: env.Now.Add(time.Hour), createdAt: env.Now.Add(-49 * time.Hour)})
	s := &fakeSender{}
	_, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	st := f.state(id)
	assert.Equal(t, "skipped", st.Status)
	assert.Equal(t, "stale", st.Reason)
}

func TestDrainDefersWholeDigestTogether(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET daily_external_cap = 1`)
	require.NoError(t, err)
	todaySent := arToday.Add(time.Hour)
	f.insert(dRow{status: "sent", sentAt: &todaySent, digest: "today", email: "other-" + f.email})
	a := f.insert(dRow{})
	b := f.insert(dRow{})
	s := &fakeSender{}
	_, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	sa, sb := f.state(a), f.state(b)
	assert.Equal(t, "planned", sa.Status)
	assert.Equal(t, "planned", sb.Status)
	assert.Equal(t, sa.RunAt, sb.RunAt)
	assert.Equal(t, nextDayWindow(env), sa.RunAt.UTC())
}
