//go:build dbtest

package automation

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrainSuppressed(t *testing.T) {
	f := newDrainFx(t)
	id := f.insert(dRow{})
	_, err := f.pool.Exec(f.ctx, `INSERT INTO automation_suppressions (email, source) VALUES ($1, 'manual')`, f.email)
	require.NoError(t, err)
	s := &fakeSender{}
	res, err := f.drain(drainEnv(), s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	assert.Equal(t, 1, res.Skipped)
	st := f.state(id)
	assert.Equal(t, "skipped", st.Status)
	assert.Equal(t, "suppressed", st.Reason)
}

func TestDrainKillSwitch(t *testing.T) {
	f := newDrainFx(t)
	id := f.insert(dRow{})
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET kill_switch = TRUE`)
	require.NoError(t, err)
	s := &fakeSender{}
	_, err = f.drain(drainEnv(), s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	st := f.state(id)
	assert.Equal(t, "skipped", st.Status)
	assert.Equal(t, "kill_switch", st.Reason)
}

func TestDrainWeeklyCapBoundary(t *testing.T) {
	env := drainEnv()
	tests := []struct {
		name     string
		sentAges []time.Duration // sent_at = Now - age, each its own digest
		wantSend bool
	}{
		{"two sent in window", []time.Duration{time.Hour, 24 * time.Hour}, true},
		{"three sent in window", []time.Duration{time.Hour, 24 * time.Hour, 6 * 24 * time.Hour}, false},
		{"third just outside window", []time.Duration{time.Hour, 24 * time.Hour, 7*24*time.Hour + time.Minute}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newDrainFx(t)
			for i, age := range tc.sentAges {
				sent := env.Now.Add(-age)
				f.insert(dRow{status: "sent", sentAt: &sent, digest: fmt.Sprintf("old-%d", i)})
			}
			id := f.insert(dRow{})
			s := &fakeSender{}
			_, err := f.drain(env, s, fakeRenderer{})
			require.NoError(t, err)
			st := f.state(id)
			if tc.wantSend {
				assert.Equal(t, "sent", st.Status)
				return
			}
			assert.Empty(t, s.reqs)
			assert.Equal(t, "skipped", st.Status)
			assert.Equal(t, "cap", st.Reason)
		})
	}
}

func TestDrainWeeklyCapCountsDigestOnce(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	sent := env.Now.Add(-time.Hour)
	for i := 0; i < 4; i++ { // four rows, one digest => counts once
		f.insert(dRow{status: "sent", sentAt: &sent, digest: "same-digest"})
	}
	id := f.insert(dRow{})
	_, err := f.drain(env, &fakeSender{}, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, "sent", f.state(id).Status)
}

func TestDrainDailyCap(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET daily_external_cap = 1`)
	require.NoError(t, err)
	before := arToday.Add(-time.Hour) // yesterday: not counted
	f.insert(dRow{status: "sent", sentAt: &before, digest: "yesterday", email: "other-" + f.email})
	id := f.insert(dRow{})
	s := &fakeSender{}
	_, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, "sent", f.state(id).Status, "yesterday's send is outside the local day")

	todaySent := arToday.Add(time.Hour)
	f.insert(dRow{status: "sent", sentAt: &todaySent, digest: "today", email: "third-" + f.email})
	id2 := f.insert(dRow{email: "next-" + f.email})
	_, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	st := f.state(id2)
	assert.Equal(t, "skipped", st.Status)
	assert.Equal(t, "cap", st.Reason)
}

func TestDrainOutsideWindowClaimsNothing(t *testing.T) {
	f := newDrainFx(t)
	id := f.insert(dRow{})
	env := drainEnv()
	env.Now = arToday.Add(3 * time.Hour)
	s := &fakeSender{}
	res, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, Result{}, res)
	assert.Empty(t, s.reqs)
	assert.Equal(t, "planned", f.state(id).Status)
}

func TestDrainMixedCaseRecipient(t *testing.T) {
	t.Run("suppression is case-insensitive", func(t *testing.T) {
		f := newDrainFx(t)
		id := f.insert(dRow{email: strings.ToUpper(f.email)})
		_, err := f.pool.Exec(f.ctx, `INSERT INTO automation_suppressions (email, source) VALUES ($1, 'manual')`, f.email)
		require.NoError(t, err)
		s := &fakeSender{}
		_, err = f.drain(drainEnv(), s, fakeRenderer{})
		require.NoError(t, err)
		assert.Empty(t, s.reqs)
		assert.Equal(t, "suppressed", f.state(id).Reason)
	})
	t.Run("weekly history counts mixed-case rows", func(t *testing.T) {
		f := newDrainFx(t)
		env := drainEnv()
		for i := 0; i < 3; i++ {
			sent := env.Now.Add(-time.Duration(i+1) * time.Hour)
			f.insert(dRow{status: "sent", sentAt: &sent, digest: fmt.Sprintf("mc-%d", i), email: strings.ToUpper(f.email)})
		}
		id := f.insert(dRow{})
		s := &fakeSender{}
		_, err := f.drain(env, s, fakeRenderer{})
		require.NoError(t, err)
		assert.Empty(t, s.reqs)
		assert.Equal(t, "cap", f.state(id).Reason)
	})
}
