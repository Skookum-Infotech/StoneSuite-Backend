//go:build dbtest

package tenancy

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedPasswordIdentity creates an identity with no password for token tests.
func seedPasswordIdentity(t *testing.T, cp *ControlPlane) *Identity {
	t.Helper()
	email := fmt.Sprintf("token-user-%d@example.com", time.Now().UnixNano())
	id, err := cp.CreateIdentity(context.Background(), seedTestTenant(t, cp), email, "", "Token User", false)
	require.NoError(t, err)
	return id
}

func TestPasswordTokenStoredHashedAndSingleUse(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	id := seedPasswordIdentity(t, cp)
	raw := fmt.Sprintf("reset-%d", time.Now().UnixNano())
	require.NoError(t, cp.SetIdentityPasswordSetupToken(ctx, id.ID, raw, time.Now().Add(time.Hour)))

	var stored string
	require.NoError(t, cp.pool.QueryRow(ctx, `SELECT password_reset_token FROM identities WHERE id = $1`, id.ID).Scan(&stored))
	assert.Equal(t, HashToken(raw), stored, "the raw token must never be persisted")

	got, err := cp.IdentityByPasswordToken(ctx, raw)
	require.NoError(t, err)
	assert.Equal(t, id.ID, got.ID)

	require.NoError(t, cp.ConsumePasswordToken(ctx, id.ID, raw, "hash-1"))
	require.ErrorIs(t, cp.ConsumePasswordToken(ctx, id.ID, raw, "hash-2"), ErrTokenAlreadyUsed)

	after, err := cp.IdentityByID(ctx, id.ID)
	require.NoError(t, err)
	assert.Equal(t, "hash-1", after.PasswordHash, "the second consume must not overwrite the password")
}

func TestPasswordTokenConcurrentConsumeHasOneWinner(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	id := seedPasswordIdentity(t, cp)
	raw := fmt.Sprintf("race-%d", time.Now().UnixNano())
	require.NoError(t, cp.SetIdentityPasswordSetupToken(ctx, id.ID, raw, time.Now().Add(time.Hour)))

	const attempts = 8
	errs := make([]error, attempts)
	var wg sync.WaitGroup
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = cp.ConsumePasswordToken(ctx, id.ID, raw, fmt.Sprintf("hash-%d", i))
		}()
	}
	wg.Wait()

	wins := 0
	for _, err := range errs {
		if err == nil {
			wins++
			continue
		}
		require.ErrorIs(t, err, ErrTokenAlreadyUsed)
	}
	assert.Equal(t, 1, wins)
}

func TestPasswordTokenLegacyAndExpiry(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()

	t.Run("pre-hashing plaintext row no longer redeems", func(t *testing.T) {
		id := seedPasswordIdentity(t, cp)
		raw := fmt.Sprintf("legacy-%d", time.Now().UnixNano())
		_, err := cp.pool.Exec(ctx, `UPDATE identities SET password_reset_token = $2, password_reset_expiry = NOW() + INTERVAL '1 hour' WHERE id = $1`, id.ID, raw)
		require.NoError(t, err)
		require.ErrorIs(t, cp.ConsumePasswordToken(ctx, id.ID, raw, "hash"), ErrTokenAlreadyUsed)
	})

	t.Run("expired token is refused", func(t *testing.T) {
		id := seedPasswordIdentity(t, cp)
		raw := fmt.Sprintf("expired-%d", time.Now().UnixNano())
		require.NoError(t, cp.SetIdentityPasswordSetupToken(ctx, id.ID, raw, time.Now().Add(-time.Minute)))
		require.ErrorIs(t, cp.ConsumePasswordToken(ctx, id.ID, raw, "hash"), ErrTokenAlreadyUsed)
	})

	t.Run("the stored hash itself is not a working token", func(t *testing.T) {
		id := seedPasswordIdentity(t, cp)
		raw := fmt.Sprintf("leak-%d", time.Now().UnixNano())
		require.NoError(t, cp.SetIdentityPasswordSetupToken(ctx, id.ID, raw, time.Now().Add(time.Hour)))
		require.ErrorIs(t, cp.ConsumePasswordToken(ctx, id.ID, HashToken(raw), "hash"), ErrTokenAlreadyUsed)
	})
}

func TestInviteTokensHashedAndClaimedOnce(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedTestTenant(t, cp)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	t.Run("tenant invite", func(t *testing.T) {
		raw := "tinv-" + suffix
		inv, err := cp.CreateInvite(ctx, tenantID, "owner-"+suffix+"@example.com", raw, time.Now().Add(time.Hour))
		require.NoError(t, err)
		assert.Equal(t, raw, inv.Token, "the minting call gets the raw token back")

		var stored string
		require.NoError(t, cp.pool.QueryRow(ctx, `SELECT token FROM tenant_invites WHERE id = $1`, inv.ID).Scan(&stored))
		assert.Equal(t, HashToken(raw), stored)

		got, err := cp.InviteByToken(ctx, raw)
		require.NoError(t, err)
		assert.Equal(t, inv.ID, got.ID)
		assert.Empty(t, got.Token, "reads must not expose the stored hash")

		require.NoError(t, cp.MarkInviteAccepted(ctx, inv.ID))
		require.ErrorIs(t, cp.MarkInviteAccepted(ctx, inv.ID), ErrTokenAlreadyUsed)
	})

	t.Run("user invite", func(t *testing.T) {
		raw := "uinv-" + suffix
		inv, err := cp.CreateUserInvite(ctx, tenantID, "member-"+suffix+"@example.com", "Member", "", raw, "", time.Now().Add(time.Hour))
		require.NoError(t, err)
		assert.Equal(t, raw, inv.Token)

		got, err := cp.UserInviteByToken(ctx, raw)
		require.NoError(t, err)
		assert.Equal(t, inv.ID, got.ID)

		require.NoError(t, cp.MarkUserInviteAccepted(ctx, inv.ID))
		require.ErrorIs(t, cp.MarkUserInviteAccepted(ctx, inv.ID), ErrTokenAlreadyUsed)
	})
}

func TestLoginThrottle(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	id := seedPasswordIdentity(t, cp)
	policy := LoginThrottle{Threshold: 3, Base: time.Minute, Max: 3 * time.Minute}

	lockedFor := func(t *testing.T) time.Duration {
		t.Helper()
		until, locked, err := cp.LoginLockedUntil(ctx, id.ID)
		require.NoError(t, err)
		if !locked {
			return 0
		}
		return time.Until(until)
	}
	approx := func(t *testing.T, want, got time.Duration) {
		t.Helper()
		assert.InDelta(t, want.Seconds(), got.Seconds(), 5, "lock duration")
	}

	for range policy.Threshold - 1 {
		require.NoError(t, cp.RecordLoginFailure(ctx, id.ID, policy))
	}
	assert.Zero(t, lockedFor(t), "below threshold must not lock")

	require.NoError(t, cp.RecordLoginFailure(ctx, id.ID, policy))
	approx(t, time.Minute, lockedFor(t))

	require.NoError(t, cp.RecordLoginFailure(ctx, id.ID, policy))
	approx(t, 2*time.Minute, lockedFor(t))

	for range 5 {
		require.NoError(t, cp.RecordLoginFailure(ctx, id.ID, policy))
	}
	approx(t, policy.Max, lockedFor(t))

	require.NoError(t, cp.ResetLoginFailures(ctx, id.ID))
	assert.Zero(t, lockedFor(t))

	var count int
	require.NoError(t, cp.pool.QueryRow(ctx, `SELECT failed_login_attempts FROM identities WHERE id = $1`, id.ID).Scan(&count))
	assert.Zero(t, count)

	_, locked, err := cp.LoginLockedUntil(ctx, "00000000-0000-0000-0000-000000000000")
	require.NoError(t, err)
	assert.False(t, locked, "unknown identity reads as unlocked")
}

func TestConsumePasswordTokenRevokesSessions(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()
	id := seedPasswordIdentity(t, cp)
	_ = seedRefreshToken(t, cp, id.ID, testRefreshFallback)
	_ = seedRefreshToken(t, cp, id.ID, testRefreshFallback)
	raw := fmt.Sprintf("reset-%d", time.Now().UnixNano())
	require.NoError(t, cp.SetIdentityPasswordSetupToken(ctx, id.ID, raw, time.Now().Add(time.Hour)))

	require.ErrorIs(t, cp.ConsumePasswordToken(ctx, id.ID, "wrong-"+raw, "hash"), ErrTokenAlreadyUsed)
	assert.Len(t, liveRefreshTokens(t, cp, id.ID), 2, "a failed consume must not end sessions")

	require.NoError(t, cp.ConsumePasswordToken(ctx, id.ID, raw, "hash"))
	assert.Empty(t, liveRefreshTokens(t, cp, id.ID), "a reset must end every session")
}
