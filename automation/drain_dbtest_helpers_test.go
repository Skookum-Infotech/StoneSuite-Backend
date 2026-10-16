//go:build dbtest

package automation

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/services"
)

type fakeSender struct {
	mu   sync.Mutex
	reqs []services.NotificationRequest
	err  error
	// onSend, when set, runs inside Send with the context the drain passed.
	onSend func(ctx context.Context)
}

func (s *fakeSender) Send(ctx context.Context, req services.NotificationRequest) (services.NotificationResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.onSend != nil {
		s.onSend(ctx)
	}
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

func (f *drainFx) count(where string, args ...any) int {
	var n int
	require.NoError(f.t, f.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM automation_deliveries WHERE rule_id = $1::uuid AND `+where, append([]any{f.ruleID}, args...)...).Scan(&n))
	return n
}
