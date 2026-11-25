package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/tenancy"
)

func TestLifetimeRefusal(t *testing.T) {
	now := time.Date(2026, 11, 25, 15, 0, 0, 0, time.UTC)
	limits := sessionLimits{idle: time.Hour, maxAge: 12 * time.Hour}
	at := func(d time.Duration) *time.Time { t := now.Add(-d); return &t }
	session := func(age, idle time.Duration) *tenancy.AuthSession {
		return &tenancy.AuthSession{CreatedAt: now.Add(-age), LastActiveAt: now.Add(-idle)}
	}
	tests := []struct {
		name     string
		s        *tenancy.AuthSession
		reported *time.Time
		limits   sessionLimits
		want     string
	}{
		{"active, young", session(time.Hour, time.Minute), nil, limits, ""},
		{"idle 59 minutes", session(2*time.Hour, 59*time.Minute), nil, limits, ""},
		{"idle 60 minutes", session(2*time.Hour, time.Hour), nil, limits, codeSessionIdle},
		{"stored idle, but the browser reports recent use", session(2*time.Hour, 3*time.Hour), at(5 * time.Minute), limits, ""},
		{"browser reports old activity", session(2*time.Hour, 2*time.Hour), at(90 * time.Minute), limits, codeSessionIdle},
		{"a report from the future counts as now, not later", session(2*time.Hour, 2*time.Hour), at(-time.Hour), limits, ""},
		{"11h59 old and active", session(12*time.Hour-time.Minute, 0), nil, limits, ""},
		{"12 hours old, active", session(12*time.Hour, 0), at(0), limits, codeSessionMaxAge},
		{"length beats inactivity", session(13*time.Hour, 2*time.Hour), nil, limits, codeSessionMaxAge},
		{"limits off", session(48*time.Hour, 24*time.Hour), nil, sessionLimits{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, lifetimeRefusal(tt.s, tt.reported, tt.limits, now))
		})
	}
}

func TestCappedTokenLifetime(t *testing.T) {
	now := time.Now()
	limits := sessionLimits{maxAge: 12 * time.Hour}
	tests := []struct {
		name string
		age  time.Duration
		l    sessionLimits
		want time.Duration
	}{
		{"plenty of time left: full hour", 2 * time.Hour, limits, time.Hour},
		{"20 minutes left: cut to 20", 12*time.Hour - 20*time.Minute, limits, 20 * time.Minute},
		{"past the end: zero", 13 * time.Hour, limits, 0},
		{"no maximum", 30 * time.Hour, sessionLimits{}, time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &tenancy.AuthSession{CreatedAt: now.Add(-tt.age)}
			assert.InDelta(t, tt.want.Seconds(), cappedTokenLifetime(time.Hour, s, tt.l, now).Seconds(), 1)
		})
	}
}

func TestLifetimeMessage(t *testing.T) {
	l := sessionLimits{idle: time.Hour, maxAge: 12 * time.Hour}
	assert.Equal(t, "You were signed out after 60 minutes of inactivity.", lifetimeMessage(codeSessionIdle, l))
	assert.Equal(t, "For security, sessions last 12 hours. Please sign in again.", lifetimeMessage(codeSessionMaxAge, l))
	assert.Equal(t, "You were signed out after 30 minutes of inactivity.",
		lifetimeMessage(codeSessionIdle, sessionLimits{idle: 30 * time.Minute}))
}
