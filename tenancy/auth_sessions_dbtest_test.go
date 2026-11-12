//go:build dbtest

package tenancy

import (
	"context"
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

	replaced, err := cp.ReplaceSessions(ctx, identity, newID, time.Hour)
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

	again, err := cp.ReplaceSessions(ctx, identity, newID, time.Hour)
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

func TestSetTenantSingleSession_DB(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)

	tenant, err := cp.TenantByID(ctx, tenantID)
	require.NoError(t, err)
	assert.True(t, tenant.SingleSessionEnforced, "on by default for customers")

	require.NoError(t, cp.SetTenantSingleSession(ctx, tenantID, false))
	tenant, err = cp.TenantByID(ctx, tenantID)
	require.NoError(t, err)
	assert.False(t, tenant.SingleSessionEnforced, "change visible through the tenant cache")

	assert.ErrorIs(t, cp.SetTenantSingleSession(ctx, "00000000-0000-0000-0000-000000000000", true), ErrTenantNotFound)
}
