package tenancy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRefreshTokenState(t *testing.T) {
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) *time.Time { v := now.Add(-d); return &v }

	tests := []struct {
		name      string
		revokedAt *time.Time
		expiresAt time.Time
		want      error
	}{
		{"active token", nil, now.Add(time.Hour), nil},
		{"expired token", nil, now.Add(-time.Second), ErrRefreshTokenNotFound},
		{"just rotated (concurrent tab)", ago(time.Second), now.Add(time.Hour), nil},
		{"rotated exactly at grace edge", ago(RefreshRotationGrace), now.Add(time.Hour), nil},
		{"rotated past grace is reuse", ago(RefreshRotationGrace + time.Second), now.Add(time.Hour), ErrRefreshTokenReused},
		{"rotated within grace but expired", ago(time.Second), now.Add(-time.Second), ErrRefreshTokenNotFound},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, refreshTokenState(tc.revokedAt, tc.expiresAt, now))
		})
	}
}

func TestRotationLifetime(t *testing.T) {
	const fallback, max = 24 * time.Hour, 720 * time.Hour
	tests := []struct {
		name   string
		stored time.Duration
		want   time.Duration
	}{
		{"normal session keeps its lifetime", 24 * time.Hour, 24 * time.Hour},
		{"remember-me session keeps its lifetime", 720 * time.Hour, 720 * time.Hour},
		{"sub-second clock skew is rounded away", 720*time.Hour + 300*time.Millisecond, 720 * time.Hour},
		{"longer than max falls back", 721 * time.Hour, fallback},
		{"non-positive falls back", 0, fallback},
		{"negative falls back", -time.Hour, fallback},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, rotationLifetime(tc.stored, fallback, max))
		})
	}
}

func TestWithIdentity(t *testing.T) {
	err := withIdentity(ErrRefreshTokenReused, "identity-1")
	assert.ErrorIs(t, err, ErrRefreshTokenReused)
	var reused *RefreshTokenReusedError
	if assert.ErrorAs(t, err, &reused) {
		assert.Equal(t, "identity-1", reused.IdentityID)
	}

	assert.Equal(t, ErrRefreshTokenNotFound, withIdentity(ErrRefreshTokenNotFound, "identity-1"))
	assert.NoError(t, withIdentity(nil, "identity-1"))
}
