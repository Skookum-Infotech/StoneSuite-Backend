//go:build dbtest

package automation

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrainSendsGroupOnce(t *testing.T) {
	f := newDrainFx(t)
	a := f.insert(dRow{stage: 1})
	b := f.insert(dRow{stage: 1})
	s := &fakeSender{}
	env := drainEnv()

	res, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Sent)
	require.Len(t, s.reqs, 1, "one email for the digest group")
	assert.Len(t, s.reqs[0].IdempotencyKey, 64)
	for _, id := range []string{a, b} {
		st := f.state(id)
		assert.Equal(t, "sent", st.Status)
		require.NotNil(t, st.SentAt)
		assert.True(t, st.SentAt.Equal(env.Now))
		assert.Nil(t, st.Lease)
		assert.Equal(t, []string{"n-1"}, st.NotifyIDs)
	}

	res, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Zero(t, res.Sent)
	assert.Len(t, s.reqs, 1, "second drain must not resend")
}

func TestDrainSupersedesEarlierStage(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	for _, earlier := range []string{"planned", "failed"} {
		t.Run(earlier, func(t *testing.T) {
			f.reset()
			stage0 := f.insert(dRow{record: "inv-9", stage: 0, status: earlier})
			stage1 := f.insert(dRow{record: "inv-9", stage: 1})
			s := &fakeSender{}
			res, err := f.drain(env, s, fakeRenderer{})
			require.NoError(t, err)
			require.Len(t, s.reqs, 1, "exactly one send")
			assert.Equal(t, NotifyKey([]string{OccurrenceKey(f.ruleID, "invoice", "inv-9", arToday, 1)}), s.reqs[0].IdempotencyKey)
			assert.Equal(t, 1, res.Sent)
			st0 := f.state(stage0)
			assert.Equal(t, "skipped", st0.Status)
			assert.Equal(t, "superseded", st0.Reason)
			assert.Equal(t, "sent", f.state(stage1).Status)
		})
	}
}

func TestDrainStaleRowsSkipped(t *testing.T) {
	f := newDrainFx(t)
	env := drainEnv()
	old := f.insert(dRow{runAt: env.Now.Add(-49 * time.Hour), createdAt: env.Now.Add(-49 * time.Hour)})
	fresh := f.insert(dRow{runAt: env.Now.Add(-47 * time.Hour), createdAt: env.Now.Add(-47 * time.Hour)})
	s := &fakeSender{}
	_, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	st := f.state(old)
	assert.Equal(t, "skipped", st.Status)
	assert.Equal(t, "stale", st.Reason)
	assert.Equal(t, "sent", f.state(fresh).Status)
	assert.Len(t, s.reqs, 1)
}

func TestDrainRendererDropsAndErrors(t *testing.T) {
	t.Run("partial drop", func(t *testing.T) {
		f := newDrainFx(t)
		keep := f.insert(dRow{record: "k", stage: 1})
		drop := f.insert(dRow{record: "d", stage: 1})
		s := &fakeSender{}
		res, err := f.drain(drainEnv(), s, fakeRenderer{drop: map[string]string{":d:" + arToday.Format("2006-01-02") + ":1": "paid"}})
		require.NoError(t, err)
		require.Len(t, s.reqs, 1)
		assert.Equal(t, NotifyKey([]string{OccurrenceKey(f.ruleID, "invoice", "k", arToday, 1)}), s.reqs[0].IdempotencyKey)
		assert.Equal(t, 1, res.Skipped)
		assert.Equal(t, "sent", f.state(keep).Status)
		st := f.state(drop)
		assert.Equal(t, "skipped", st.Status)
		assert.Equal(t, "paid", st.Reason)
	})
	t.Run("all dropped", func(t *testing.T) {
		f := newDrainFx(t)
		id := f.insert(dRow{record: "d", stage: 1})
		s := &fakeSender{}
		_, err := f.drain(drainEnv(), s, fakeRenderer{drop: map[string]string{":1": "paid"}})
		require.NoError(t, err)
		assert.Empty(t, s.reqs)
		assert.Equal(t, "paid", f.state(id).Reason)
	})
	t.Run("render error releases", func(t *testing.T) {
		f := newDrainFx(t)
		id := f.insert(dRow{attempt: 2, status: "failed"})
		s := &fakeSender{}
		_, err := f.drain(drainEnv(), s, fakeRenderer{err: errors.New("render boom")})
		require.Error(t, err)
		assert.Empty(t, s.reqs)
		st := f.state(id)
		assert.Equal(t, "planned", st.Status)
		assert.Equal(t, 2, st.Attempts)
		assert.Nil(t, st.Lease)
	})
}

func TestDrainNeverSplitsDigestAcrossBatches(t *testing.T) {
	f := newDrainFx(t)
	var ids [2][]string
	for n, size := range []int{26, 25} { // 51 due rows, batch cap 50
		for i := 0; i < size; i++ {
			ids[n] = append(ids[n], f.insert(dRow{digest: []string{"d-a", "d-b"}[n], email: []string{"a." + f.email, "b." + f.email}[n]}))
		}
	}
	s := &fakeSender{}
	env := drainEnv()
	res, err := f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Sent, "first pass claims one whole digest")
	require.Len(t, s.reqs, 1)
	for _, id := range ids[1] {
		assert.Equal(t, "planned", f.state(id).Status, "second digest untouched")
	}
	_, err = f.drain(env, s, fakeRenderer{})
	require.NoError(t, err)
	require.Len(t, s.reqs, 2, "each digest is exactly one email")
	assert.NotEqual(t, s.reqs[0].IdempotencyKey, s.reqs[1].IdempotencyKey)
	for _, grp := range ids {
		for _, id := range grp {
			assert.Equal(t, "sent", f.state(id).Status)
		}
	}
}

func TestDrainOversizedDigestClaimedWhole(t *testing.T) {
	f := newDrainFx(t)
	for i := 0; i < claimBatchSize+5; i++ {
		f.insert(dRow{})
	}
	s := &fakeSender{}
	res, err := f.drain(drainEnv(), s, fakeRenderer{})
	require.NoError(t, err)
	assert.Equal(t, 1, res.Sent)
	assert.Len(t, s.reqs, 1)
	assert.Zero(t, f.count(`status <> 'sent'`))
}

func TestDrainSupersedesExpiredClaimedLowerStage(t *testing.T) {
	f := newDrainFx(t)
	expired := drainEnv().Now.Add(-time.Minute)
	low := f.insert(dRow{record: "inv-7", stage: 0, status: "claimed", lease: &expired})
	f.insert(dRow{record: "inv-7", stage: 1})
	s := &fakeSender{}
	_, err := f.drain(drainEnv(), s, fakeRenderer{})
	require.NoError(t, err)
	assert.Len(t, s.reqs, 1)
	st := f.state(low)
	assert.Equal(t, "skipped", st.Status)
	assert.Equal(t, "superseded", st.Reason)
}
