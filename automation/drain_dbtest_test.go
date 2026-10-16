//go:build dbtest

package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/services"
)

type fakeSender struct {
	mu   sync.Mutex
	reqs []services.NotificationRequest
	err  error
}

func (s *fakeSender) Send(_ context.Context, req services.NotificationRequest) (services.NotificationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if s.err != nil {
		return services.NotificationResult{}, s.err
	}
	return services.NotificationResult{NotificationIDs: []string{fmt.Sprintf("n-%d", len(s.reqs))}}, nil
}

type fakeRenderer struct {
	drop map[string]string // occurrence-key suffix -> reason
	err  error
}

func (r fakeRenderer) Render(_ context.Context, _ JobEnv, g Group) (services.NotificationRequest, []Delivery, map[string]string, error) {
	if r.err != nil {
		return services.NotificationRequest{}, nil, nil, r.err
	}
	var kept []Delivery
	dropped := map[string]string{}
	for _, it := range g.Items {
		reason := ""
		for suffix, why := range r.drop {
			if strings.HasSuffix(it.OccurrenceKey, suffix) {
				reason = why
			}
		}
		if reason != "" {
			dropped[it.OccurrenceKey] = reason
		} else {
			kept = append(kept, it)
		}
	}
	return services.NotificationRequest{Title: "reminder"}, kept, dropped, nil
}

type drainFx struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	ruleID string
	email  string
	n      int
}

func drainEnv() JobEnv {
	return JobEnv{Now: arToday.Add(10 * time.Hour), Today: arToday, Loc: time.UTC}
}

func newDrainFx(t *testing.T) *drainFx {
	t.Helper()
	pool := newTenantTestPool(t)
	ctx := context.Background()
	f := &drainFx{t: t, ctx: ctx, pool: pool, email: strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "@x.test"}
	key := "test.drain." + t.Name()
	require.NoError(t, pool.QueryRow(ctx, `
INSERT INTO automation_rules (rule_key, name, audience) VALUES ($1, 'drain test', 'external')
ON CONFLICT (rule_key) DO UPDATE SET name = EXCLUDED.name RETURNING id::text`, key).Scan(&f.ruleID))
	f.reset()
	t.Cleanup(func() {
		f.reset()
		_, _ = pool.Exec(ctx, `DELETE FROM automation_rules WHERE id = $1::uuid`, f.ruleID)
	})
	return f
}

// reset removes this test's rows/suppressions and restores the default settings.
func (f *drainFx) reset() {
	_, err := f.pool.Exec(f.ctx, `DELETE FROM automation_deliveries WHERE rule_id = $1::uuid`, f.ruleID)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(f.ctx, `DELETE FROM automation_suppressions WHERE email = $1`, f.email)
	require.NoError(f.t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE automation_settings SET kill_switch = FALSE, daily_external_cap = 200, per_contact_weekly_cap = 3`)
	require.NoError(f.t, err)
}

type dRow struct {
	record  string
	stage   int
	status  string
	runAt   time.Time
	digest  string
	attempt int
	lease   *time.Time
	sentAt  *time.Time
	email   string
}

func (f *drainFx) insert(r dRow) string {
	f.n++
	if r.record == "" {
		r.record = fmt.Sprintf("inv-%d", f.n)
	}
	if r.status == "" {
		r.status = "planned"
	}
	if r.runAt.IsZero() {
		r.runAt = drainEnv().Now.Add(-time.Minute)
	}
	if r.email == "" {
		r.email = f.email
	}
	if r.digest == "" {
		r.digest = DigestKey(f.ruleID, r.email, arToday)
	}
	anchor := arToday
	var id string
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `
INSERT INTO automation_deliveries (rule_id, resource, record_id, anchor_date, stage, occurrence_key, recipient_kind,
  recipient_email, digest_key, run_at, status, attempts, lease_until, sent_at)
VALUES ($1::uuid, 'invoice', $2, $3::date, $4, $5, 'external', $6, $7, $8, $9, $10, $11, $12) RETURNING id::text`,
		f.ruleID, r.record, anchor, r.stage, OccurrenceKey(f.ruleID, "invoice", r.record, anchor, r.stage),
		r.email, r.digest, r.runAt, r.status, r.attempt, r.lease, r.sentAt).Scan(&id))
	return id
}

type dState struct {
	Status, Reason, LastErr string
	Attempts                int
	RunAt                   time.Time
	SentAt, Lease           *time.Time
	NotifyIDs               []string
}

func (f *drainFx) state(id string) dState {
	var s dState
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `
SELECT status, COALESCE(reason, ''), COALESCE(last_error, ''), attempts, run_at, sent_at, lease_until, notify_notification_ids
FROM automation_deliveries WHERE id = $1::uuid`, id).Scan(&s.Status, &s.Reason, &s.LastErr, &s.Attempts, &s.RunAt, &s.SentAt, &s.Lease, &s.NotifyIDs))
	return s
}

func (f *drainFx) drain(env JobEnv, s Sender, r Renderer) (Result, error) {
	return drain(f.ctx, f.pool, env, s, r, f.ruleID)
}

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
	old := f.insert(dRow{runAt: env.Now.Add(-49 * time.Hour)})
	fresh := f.insert(dRow{runAt: env.Now.Add(-47 * time.Hour)})
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
