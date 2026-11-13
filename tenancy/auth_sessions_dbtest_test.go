//go:build dbtest

package tenancy

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSessionFixture returns a control plane and an identity to hang sessions off.
func newSessionFixture(t *testing.T) (*ControlPlane, string) {
	t.Helper()
	cp := newCPTestControlPlane(t)
	return cp, seedRefreshTestIdentity(t, cp)
}

func createTestSession(t *testing.T, cp *ControlPlane, identityID, label string) string {
	t.Helper()
	id, err := cp.CreateSession(context.Background(), NewSessionInput{IdentityID: identityID, DeviceLabel: label})
	require.NoError(t, err)
	return id
}

func TestAuthSessions_ReplaceEndsOthersAfterGrace_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	oldID := createTestSession(t, cp, identity, "Chrome on Windows")
	newID := createTestSession(t, cp, identity, "Edge on Windows")

	replaced, err := cp.ReplaceSessions(ctx, identity, newID, time.Hour, ReplaceConfirmed)
	require.NoError(t, err)
	assert.Equal(t, []string{oldID}, replaced)

	old, err := cp.SessionByID(ctx, oldID)
	require.NoError(t, err)
	assert.Equal(t, SessionReplacing, old.Status(time.Now()))
	assert.Equal(t, newID, old.ReplacedBy)
	assert.Equal(t, SessionEnded, old.Status(time.Now().Add(2*time.Hour)))

	live, err := cp.LiveSessionFor(ctx, identity)
	require.NoError(t, err)
	assert.Equal(t, newID, live.ID, "only the new session is live")

	again, err := cp.ReplaceSessions(ctx, identity, newID, time.Hour, ReplaceConfirmed)
	require.NoError(t, err)
	assert.Empty(t, again, "already-replaced sessions are not replaced twice")
}

func TestAuthSessions_EndAndTouch_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	id := createTestSession(t, cp, identity, "")

	_, err := cp.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, id)
	require.NoError(t, err)
	require.NoError(t, cp.TouchSession(ctx, id))
	s, err := cp.SessionByID(ctx, id)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now(), s.LastSeenAt, time.Minute)

	require.NoError(t, cp.EndSession(ctx, id))
	require.NoError(t, cp.EndSession(ctx, id), "ending twice is not an error")
	s, err = cp.SessionByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, SessionEnded, s.Status(time.Now()))
	_, err = cp.LiveSessionFor(ctx, identity)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestTakeoverTicket_SingleUse_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	hash := HashRefreshToken("ticket-" + identity)
	require.NoError(t, cp.CreateTakeoverTicket(ctx, hash, identity, true, time.Minute))

	got, remember, err := cp.ConsumeTakeoverTicket(ctx, hash)
	require.NoError(t, err)
	assert.Equal(t, identity, got)
	assert.True(t, remember)

	_, _, err = cp.ConsumeTakeoverTicket(ctx, hash)
	assert.ErrorIs(t, err, ErrTakeoverTicketNotFound)

	expired := HashRefreshToken("expired-" + identity)
	require.NoError(t, cp.CreateTakeoverTicket(ctx, expired, identity, false, -time.Second))
	_, _, err = cp.ConsumeTakeoverTicket(ctx, expired)
	assert.ErrorIs(t, err, ErrTakeoverTicketNotFound)
}

func TestRotateRefreshToken_CarriesSession_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	sid := createTestSession(t, cp, identity, "")
	old := HashRefreshToken("carry-old-" + sid)
	require.NoError(t, cp.CreateSessionRefreshToken(ctx, identity, sid, old, time.Now().Add(time.Hour)))

	rec, err := cp.RefreshTokenByHash(ctx, old)
	require.NoError(t, err)
	assert.Equal(t, sid, rec.SessionID)

	next := HashRefreshToken("carry-next-" + sid)
	rotated, err := cp.RotateRefreshToken(ctx, old, next, testRefreshFallback, testRefreshMax)
	require.NoError(t, err)
	assert.Equal(t, sid, rotated.SessionID)
	got, err := cp.RefreshTokenByHash(ctx, next)
	require.NoError(t, err)
	assert.Equal(t, sid, got.SessionID, "normal rotation keeps the session")

	// Replaying the rotated token inside the grace window replaces the
	// successor; the replacement keeps the session too.
	replay := HashRefreshToken("carry-replay-" + sid)
	graced, err := cp.RotateRefreshToken(ctx, old, replay, testRefreshFallback, testRefreshMax)
	require.NoError(t, err)
	require.True(t, graced.GraceReplay)
	got, err = cp.RefreshTokenByHash(ctx, replay)
	require.NoError(t, err)
	assert.Equal(t, sid, got.SessionID, "grace-replay rotation keeps the session")
}

func TestAttachRefreshTokenSession_OnlyLegacy_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	legacy := seedRefreshToken(t, cp, identity, time.Hour)
	sid := createTestSession(t, cp, identity, "")
	other := createTestSession(t, cp, identity, "")

	require.NoError(t, cp.AttachRefreshTokenSession(ctx, legacy, sid))
	rec, err := cp.RefreshTokenByHash(ctx, legacy)
	require.NoError(t, err)
	assert.Equal(t, sid, rec.SessionID)

	require.NoError(t, cp.AttachRefreshTokenSession(ctx, legacy, other))
	rec, err = cp.RefreshTokenByHash(ctx, legacy)
	require.NoError(t, err)
	assert.Equal(t, sid, rec.SessionID, "a token that has a session keeps it")
}

func TestUpdateTenantSessionPolicy_DB(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	b := func(v bool) *bool { return &v }
	n := func(v int) *int { return &v }

	tenant, err := cp.TenantByID(ctx, tenantID)
	require.NoError(t, err)
	assert.True(t, tenant.SingleSessionEnforced, "on by default for customers")
	assert.Equal(t, 5, tenant.TakeoverCooldownMinutes, "default wait: 5 minutes")
	assert.Equal(t, 5, tenant.TakeoverDailyCap, "default cap: 5 a day")

	_, err = cp.UpdateTenantSessionPolicy(ctx, tenantID, SessionPolicyPatch{CooldownMinutes: n(2)})
	require.NoError(t, err)
	tenant, err = cp.TenantByID(ctx, tenantID)
	require.NoError(t, err)
	assert.Equal(t, 2, tenant.TakeoverCooldownMinutes)
	assert.Equal(t, 5, tenant.TakeoverDailyCap, "fields left out are unchanged")
	assert.True(t, tenant.SingleSessionEnforced, "fields left out are unchanged")

	_, err = cp.UpdateTenantSessionPolicy(ctx, tenantID, SessionPolicyPatch{Enforced: b(false), DailyCap: n(0)})
	require.NoError(t, err)
	tenant, err = cp.TenantByID(ctx, tenantID)
	require.NoError(t, err)
	assert.False(t, tenant.SingleSessionEnforced, "change visible through the tenant cache")
	assert.Equal(t, 0, tenant.TakeoverDailyCap)

	_, err = cp.UpdateTenantSessionPolicy(ctx, tenantID, SessionPolicyPatch{DailyCap: n(1000)})
	assert.Error(t, err,
		"the CHECK constraint backs up the handler's range check")
	_, err = cp.UpdateTenantSessionPolicy(ctx, "00000000-0000-0000-0000-000000000000", SessionPolicyPatch{Enforced: b(true)})
	assert.ErrorIs(t, err, ErrTenantNotFound)
}

func TestStartSession_ConcurrentSignInsLeaveOneLive_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	const signIns = 8
	var wg sync.WaitGroup
	errs := make(chan error, signIns)
	for range signIns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := cp.StartSession(ctx, StartSessionInput{
				NewSessionInput: NewSessionInput{IdentityID: identity},
				Enforced:        true, Grace: time.Minute, Reason: ReplaceConfirmed,
			})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	var live int
	require.NoError(t, cp.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM auth_sessions WHERE identity_id = $1 AND ended_at IS NULL AND replaced_at IS NULL`,
		identity).Scan(&live))
	assert.Equal(t, 1, live, "exactly one session is live after simultaneous sign-ins")
}

func TestStartSession_PrunesOldSessions_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	old := createTestSession(t, cp, identity, "")
	recent := createTestSession(t, cp, identity, "")
	_, err := cp.pool.Exec(ctx, `UPDATE auth_sessions SET ended_at = NOW() - INTERVAL '91 days' WHERE id = $1`, old)
	require.NoError(t, err)
	require.NoError(t, cp.EndSession(ctx, recent))

	_, err = cp.StartSession(ctx, StartSessionInput{NewSessionInput: NewSessionInput{IdentityID: identity}})
	require.NoError(t, err)

	_, err = cp.SessionByID(ctx, old)
	assert.ErrorIs(t, err, ErrSessionNotFound, "ended more than 90 days ago: pruned")
	_, err = cp.SessionByID(ctx, recent)
	assert.NoError(t, err, "recently ended: kept")
}

func TestMovesSince_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	start := func(reason string) {
		_, err := cp.StartSession(ctx, StartSessionInput{
			NewSessionInput: NewSessionInput{IdentityID: identity}, Enforced: true, Grace: time.Minute, Reason: reason,
		})
		require.NoError(t, err)
	}
	backdateLive := func(d time.Duration) {
		_, err := cp.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = NOW() - $2::bigint * INTERVAL '1 second'
			WHERE identity_id = $1 AND ended_at IS NULL AND replaced_at IS NULL`, identity, int64(d/time.Second))
		require.NoError(t, err)
	}
	start(ReplaceIdle)      // first session: replaces nothing, no move
	start(ReplaceConfirmed) // move 1: confirmed
	start(ReplaceIdle)      // move 2: silently replaced a session used moments ago
	backdateLive(3 * time.Hour)
	start(ReplaceIdle) // not a move: the replaced browser was abandoned
	backdateLive(3 * time.Hour)
	start(ReplaceConfirmed) // move 3: confirmed counts whatever the age

	h, err := cp.MovesSince(ctx, identity, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 3, h.Count)
	require.NotNil(t, h.Last)
	assert.WithinDuration(t, time.Now(), *h.Last, time.Minute)

	h, err = cp.MovesSince(ctx, identity, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 0, h.Count)
	assert.Nil(t, h.Last)
}

func TestStartSession_EnforcesLimitsUnderLock_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	in := StartSessionInput{
		NewSessionInput: NewSessionInput{IdentityID: identity}, Enforced: true, Grace: time.Minute,
		Reason: ReplaceIdle, Limits: TakeoverLimits{DailyCap: 2},
	}
	for range 3 { // first sign-in + 2 moves
		_, err := cp.StartSession(ctx, in)
		require.NoError(t, err)
	}
	_, err := cp.StartSession(ctx, in)
	var limited *TakeoverLimitError
	require.ErrorAs(t, err, &limited, "a silent replacement of a session in use is a move too")
	assert.Equal(t, CodeTakeoverLimit, limited.Code)

	// Simultaneous confirmations cannot overshoot the cap either.
	other := seedRefreshTestIdentity(t, cp)
	in.IdentityID, in.Reason, in.Limits = other, ReplaceConfirmed, TakeoverLimits{DailyCap: 1}
	_, err = cp.StartSession(ctx, in)
	require.NoError(t, err)
	var wg sync.WaitGroup
	var ok atomic.Int32
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := cp.StartSession(ctx, in); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), ok.Load(), "exactly one move fits a cap of 1")
}

func TestStartSession_LegacyAdoptionNeverReplacesASessionInUse_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	live := createTestSession(t, cp, identity, "")
	in := StartSessionInput{
		NewSessionInput: NewSessionInput{IdentityID: identity}, Enforced: true, Grace: time.Minute,
		Reason: ReplaceLegacy, RefuseIfLive: true,
	}
	_, err := cp.StartSession(ctx, in)
	assert.ErrorIs(t, err, ErrLiveSessionExists)
	s, err := cp.SessionByID(ctx, live)
	require.NoError(t, err)
	assert.Equal(t, SessionActive, s.Status(time.Now()), "the session in use keeps going")

	_, err = cp.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = NOW() - INTERVAL '3 hours' WHERE id = $1`, live)
	require.NoError(t, err)
	_, err = cp.StartSession(ctx, in)
	assert.NoError(t, err, "an abandoned session does not block adoption")
}

func TestRevocationsEndSessions_DB(t *testing.T) {
	cp, identity := newSessionFixture(t)
	ctx := context.Background()
	live := func() int {
		var n int
		require.NoError(t, cp.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM auth_sessions WHERE identity_id = $1 AND ended_at IS NULL`, identity).Scan(&n))
		return n
	}
	a := createTestSession(t, cp, identity, "")
	b := createTestSession(t, cp, identity, "")
	keep := HashRefreshToken("keep-" + a)
	require.NoError(t, cp.CreateSessionRefreshToken(ctx, identity, a, keep, time.Now().Add(time.Hour)))
	require.NoError(t, cp.CreateSessionRefreshToken(ctx, identity, b, HashRefreshToken("other-"+b), time.Now().Add(time.Hour)))

	require.NoError(t, cp.SetIdentityPasswordRevokingSessions(ctx, identity, "new-hash", keep))
	sa, err := cp.SessionByID(ctx, a)
	require.NoError(t, err)
	sb, err := cp.SessionByID(ctx, b)
	require.NoError(t, err)
	assert.Equal(t, SessionActive, sa.Status(time.Now()), "password change keeps the current session")
	assert.Equal(t, SessionEnded, sb.Status(time.Now()), "and ends the others")

	require.NoError(t, cp.RevokeAllRefreshTokens(ctx, identity))
	assert.Equal(t, 0, live(), "revoke-all ends every session")
	_, err = cp.LiveSessionFor(ctx, identity)
	assert.ErrorIs(t, err, ErrSessionNotFound, "so the next sign-in elsewhere is not asked")
}

func TestUpdateTenantSessionPolicy_SwitchingOnEndsDuplicates_DB(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	off, on := false, true
	_, err := cp.UpdateTenantSessionPolicy(ctx, tenantID, SessionPolicyPatch{Enforced: &off})
	require.NoError(t, err)

	email := fmt.Sprintf("dup-%d@example.com", time.Now().UnixNano())
	ident, err := cp.CreateIdentity(ctx, tenantID, email, "x", "Dup", true)
	require.NoError(t, err)
	older := createTestSession(t, cp, ident.ID, "Chrome on Windows")
	newer := createTestSession(t, cp, ident.ID, "Edge on Windows")
	_, err = cp.pool.Exec(ctx, `UPDATE auth_sessions SET last_seen_at = NOW() - INTERVAL '10 minutes' WHERE id = $1`, older)
	require.NoError(t, err)

	ended, err := cp.UpdateTenantSessionPolicy(ctx, tenantID, SessionPolicyPatch{Enforced: &on, Grace: time.Minute})
	require.NoError(t, err)
	assert.Equal(t, []string{older}, ended, "the less recently used browser is ended")
	s, err := cp.SessionByID(ctx, older)
	require.NoError(t, err)
	assert.Equal(t, SessionReplacing, s.Status(time.Now()), "with the usual countdown")
	assert.Equal(t, newer, s.ReplacedBy)

	h, err := cp.MovesSince(ctx, ident.ID, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 0, h.Count, "a policy switch is not the user moving their sign-in")

	ended, err = cp.UpdateTenantSessionPolicy(ctx, tenantID, SessionPolicyPatch{Enforced: &on})
	require.NoError(t, err)
	assert.Empty(t, ended, "already on: nothing more is ended")
}
