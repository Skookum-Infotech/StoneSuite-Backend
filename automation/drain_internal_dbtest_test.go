//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exhaustCaps sets weekly and daily caps to 1, adds one already-sent external
// row for the fixture recipient and one for another recipient, and suppresses
// the fixture recipient.
func exhaustCaps(t *testing.T, f *drainFx) {
	t.Helper()
	sent := drainEnv().Now.Add(-time.Hour)
	f.insert(dRow{status: "sent", sentAt: &sent})
	f.insert(dRow{status: "sent", sentAt: &sent, email: "other-" + f.email})
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET daily_external_cap = 1, per_contact_weekly_cap = 1`)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `INSERT INTO automation_suppressions (email, source) VALUES ($1, 'manual')`, f.email)
	require.NoError(t, err)
}

func TestDrainInternalIgnoresCapsAndSuppression(t *testing.T) {
	t.Run("internal row still sends", func(t *testing.T) {
		f := newDrainFx(t)
		exhaustCaps(t, f)
		id := f.insert(dRow{kind: recipientInternal})
		s := &fakeSender{}
		res, err := f.drain(drainEnv(), s, fakeRenderer{})
		require.NoError(t, err)
		assert.Len(t, s.reqs, 1)
		assert.Equal(t, 1, res.Sent)
		assert.Equal(t, "sent", f.state(id).Status)
	})
	t.Run("external row is still guarded", func(t *testing.T) {
		f := newDrainFx(t)
		exhaustCaps(t, f)
		id := f.insert(dRow{})
		s := &fakeSender{}
		res, err := f.drain(drainEnv(), s, fakeRenderer{})
		require.NoError(t, err)
		assert.Empty(t, s.reqs)
		assert.Equal(t, 1, res.Skipped)
		st := f.state(id)
		assert.Equal(t, "skipped", st.Status)
		assert.Equal(t, "suppressed", st.Reason)
	})
}

func TestDrainInternalKillSwitchDefers(t *testing.T) {
	f := newDrainFx(t)
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET kill_switch = TRUE`)
	require.NoError(t, err)
	id := f.insert(dRow{kind: recipientInternal})
	s := &fakeSender{}
	res, err := f.drain(drainEnv(), s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	assert.Zero(t, res.Skipped)
	st := f.state(id)
	assert.Equal(t, "planned", st.Status)
	assert.True(t, st.RunAt.After(drainEnv().Now))
	assert.Nil(t, st.Lease)
}
