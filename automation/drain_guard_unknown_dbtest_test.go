//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// insertUnknown inserts an unknown-outcome row (sent_at NULL) whose updated_at is at.
func (f *drainFx) insertUnknown(digest, email string, at time.Time) {
	id := f.insert(dRow{status: "unknown", digest: digest, email: email})
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_deliveries SET updated_at = $2 WHERE id = $1::uuid`, id, at)
	require.NoError(f.t, err)
}

func TestWeeklyCapCountsUnknown(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET per_contact_weekly_cap = 1`)
	require.NoError(t, err)
	f.insertUnknown("unknown-digest", f.email, env.Now.Add(-24*time.Hour))
	id := f.insert(dRow{})
	s := &fakeSender{}
	_, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	st := f.state(id)
	assert.Equal(t, "planned", st.Status)
	assert.Equal(t, nextDayWindow(env), st.RunAt.UTC())
}

func TestDailyCapCountsUnknown(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET daily_external_cap = 1`)
	require.NoError(t, err)
	f.insertUnknown("unknown-today", "other-"+f.email, arToday.Add(time.Hour))
	id := f.insert(dRow{})
	s := &fakeSender{}
	_, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Empty(t, s.reqs)
	st := f.state(id)
	assert.Equal(t, "planned", st.Status)
	assert.Equal(t, nextDayWindow(env), st.RunAt.UTC())
}
