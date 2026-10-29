package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
)

// withTokenLifetimes sets the three token-lifetime env values for one test.
func withTokenLifetimes(t *testing.T, access, refresh, rememberMe string) {
	t.Helper()
	orig := config.AppConfig
	config.AppConfig.JWTExpiresIn = access
	config.AppConfig.RefreshTokenExpiresIn = refresh
	config.AppConfig.JWTRememberMeExpiresIn = rememberMe
	t.Cleanup(func() {
		config.AppConfig.JWTExpiresIn = orig.JWTExpiresIn
		config.AppConfig.RefreshTokenExpiresIn = orig.RefreshTokenExpiresIn
		config.AppConfig.JWTRememberMeExpiresIn = orig.JWTRememberMeExpiresIn
	})
}

func TestTokenLifetimes(t *testing.T) {
	tests := []struct {
		name                          string
		access, refresh, rememberMe   string
		wantAccess                    time.Duration
		wantRefresh, wantRememberedRT time.Duration
	}{
		{"configured", "1h", "24h", "720h", time.Hour, 24 * time.Hour, 720 * time.Hour},
		{"unparseable falls back", "nope", "", "bad", defaultAccessTokenLifetime, defaultRefreshTokenLifetime, defaultRememberMeLifetime},
		{"remember-me never shortens a session", "15m", "48h", "1h", 15 * time.Minute, 48 * time.Hour, 48 * time.Hour},
		{"non-positive falls back", "0s", "-1h", "0s", defaultAccessTokenLifetime, defaultRefreshTokenLifetime, defaultRememberMeLifetime},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			withTokenLifetimes(t, tc.access, tc.refresh, tc.rememberMe)
			assert.Equal(t, tc.wantAccess, accessTokenLifetime())
			assert.Equal(t, tc.wantRefresh, refreshTokenLifetime(false))
			assert.Equal(t, tc.wantRememberedRT, refreshTokenLifetime(true))
		})
	}
}
