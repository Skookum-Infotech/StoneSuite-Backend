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
