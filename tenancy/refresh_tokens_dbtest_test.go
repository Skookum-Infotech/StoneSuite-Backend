//go:build dbtest

package tenancy

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRefreshFallback = 24 * time.Hour
	testRefreshMax      = 720 * time.Hour
)

// seedRefreshTestIdentity creates an identity to hang refresh tokens off.
func seedRefreshTestIdentity(t *testing.T, cp *ControlPlane) string {
	t.Helper()
	email := fmt.Sprintf("refresh-%d@example.com", time.Now().UnixNano())
	id, err := cp.CreateIdentity(context.Background(), seedTestTenant(t, cp), email, "x", "Refresh Test", true)
	require.NoError(t, err)
	return id.ID
}

// seedRefreshToken stores a token with the given lifetime and returns its hash.
func seedRefreshToken(t *testing.T, cp *ControlPlane, identityID string, lifetime time.Duration) string {
	t.Helper()
	hash := HashRefreshToken(fmt.Sprintf("raw-%d", time.Now().UnixNano()))
	require.NoError(t, cp.CreateRefreshToken(context.Background(), identityID, hash, time.Now().Add(lifetime)))
	return hash
}

// liveRefreshTokens returns the hashes of identityID's unrevoked tokens.
func liveRefreshTokens(t *testing.T, cp *ControlPlane, identityID string) []string {
	t.Helper()
	rows, err := cp.pool.Query(context.Background(),
		`SELECT token_hash FROM refresh_tokens WHERE identity_id = $1 AND revoked_at IS NULL`, identityID)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		require.NoError(t, rows.Scan(&h))
		out = append(out, h)
	}
	require.NoError(t, rows.Err())
	return out
}

func newHash(label string) string {
	return HashRefreshToken(fmt.Sprintf("%s-%d", label, time.Now().UnixNano()))
}

func TestRotateRefreshToken_KeepsRememberMeLifetime(t *testing.T) {
	cp := newCPTestControlPlane(t)
	identityID := seedRefreshTestIdentity(t, cp)
	old := seedRefreshToken(t, cp, identityID, testRefreshMax)

	rec, err := cp.RotateRefreshToken(context.Background(), old, newHash("next"), testRefreshFallback, testRefreshMax)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(testRefreshMax), rec.ExpiresAt, time.Minute)
	assert.False(t, rec.GraceReplay)
	assert.Equal(t, identityID, rec.IdentityID)
}

// A replay inside the grace window must replace the successor, never add a
// second live token next to it.
func TestRotateRefreshToken_GraceReplayKeepsOneLiveToken(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identityID := seedRefreshTestIdentity(t, cp)
	old := seedRefreshToken(t, cp, identityID, testRefreshFallback)

	first := newHash("first")
	_, err := cp.RotateRefreshToken(ctx, old, first, testRefreshFallback, testRefreshMax)
	require.NoError(t, err)
	assert.Equal(t, []string{first}, liveRefreshTokens(t, cp, identityID))

	for _, label := range []string{"second", "third"} {
		replay := newHash(label)
		rec, err := cp.RotateRefreshToken(ctx, old, replay, testRefreshFallback, testRefreshMax)
		require.NoError(t, err, label)
		assert.Equal(t, []string{replay}, liveRefreshTokens(t, cp, identityID), label)
		assert.True(t, rec.GraceReplay, label)
		assert.WithinDuration(t, time.Now().Add(testRefreshFallback), rec.ExpiresAt, time.Minute, label)
	}
}

// After every session is revoked, an old token inside the grace window has no
// live successor and must not mint a new session.
func TestRotateRefreshToken_GraceReplayAfterRevokeAllIsSuperseded(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identityID := seedRefreshTestIdentity(t, cp)
	rotated := seedRefreshToken(t, cp, identityID, testRefreshFallback)
	untouched := seedRefreshToken(t, cp, identityID, testRefreshFallback)
	_, err := cp.RotateRefreshToken(ctx, rotated, newHash("next"), testRefreshFallback, testRefreshMax)
	require.NoError(t, err)

	require.NoError(t, cp.RevokeAllRefreshTokens(ctx, identityID))

	for _, h := range []string{rotated, untouched} {
		_, err := cp.RotateRefreshToken(ctx, h, newHash("replay"), testRefreshFallback, testRefreshMax)
		assert.ErrorIs(t, err, ErrRefreshTokenSuperseded)
	}
	assert.Empty(t, liveRefreshTokens(t, cp, identityID))
}

func TestRefreshToken_ReusePastGraceNamesIdentity(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	identityID := seedRefreshTestIdentity(t, cp)
	h := seedRefreshToken(t, cp, identityID, testRefreshFallback)
	require.NoError(t, cp.RevokeRefreshTokenNow(ctx, h))

	_, err := cp.RefreshTokenByHash(ctx, h)
	var reused *RefreshTokenReusedError
	require.ErrorAs(t, err, &reused)
	assert.Equal(t, identityID, reused.IdentityID)

	_, err = cp.RotateRefreshToken(ctx, h, newHash("next"), testRefreshFallback, testRefreshMax)
	require.ErrorAs(t, err, &reused)
	assert.Equal(t, identityID, reused.IdentityID)
	assert.Empty(t, liveRefreshTokens(t, cp, identityID), "a reused token must not mint a session")
}

func TestSetIdentityPasswordRevokingSessions(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()

	t.Run("change keeps only the caller's session", func(t *testing.T) {
		identityID := seedRefreshTestIdentity(t, cp)
		current := seedRefreshToken(t, cp, identityID, testRefreshFallback)
		_ = seedRefreshToken(t, cp, identityID, testRefreshFallback)
		require.NoError(t, cp.SetIdentityPasswordRevokingSessions(ctx, identityID, "new-hash", current))
		assert.Equal(t, []string{current}, liveRefreshTokens(t, cp, identityID))
	})

	t.Run("reset revokes every session", func(t *testing.T) {
		identityID := seedRefreshTestIdentity(t, cp)
		_ = seedRefreshToken(t, cp, identityID, testRefreshFallback)
		_ = seedRefreshToken(t, cp, identityID, testRefreshFallback)
		require.NoError(t, cp.SetIdentityPasswordRevokingSessions(ctx, identityID, "new-hash", ""))
		assert.Empty(t, liveRefreshTokens(t, cp, identityID))
		got, err := cp.IdentityByID(ctx, identityID)
		require.NoError(t, err)
		assert.Equal(t, "new-hash", got.PasswordHash)
	})

	t.Run("keep hash of another identity does not spare it", func(t *testing.T) {
		victim := seedRefreshTestIdentity(t, cp)
		other := seedRefreshTestIdentity(t, cp)
		otherToken := seedRefreshToken(t, cp, other, testRefreshFallback)
		_ = seedRefreshToken(t, cp, victim, testRefreshFallback)
		require.NoError(t, cp.SetIdentityPasswordRevokingSessions(ctx, victim, "new-hash", otherToken))
		assert.Empty(t, liveRefreshTokens(t, cp, victim))
		assert.Equal(t, []string{otherToken}, liveRefreshTokens(t, cp, other), "another identity's tokens are untouched")
	})
}
