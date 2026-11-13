package controllers

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/tenancy"
)

func TestSingleSessionApplies(t *testing.T) {
	tests := []struct {
		name                            string
		enforced, platformAdmin, portal bool
		want                            bool
	}{
		{"customer staff, enforced", true, false, false, true},
		{"customer staff, setting off", false, false, false, false},
		{"platform admin is exempt", true, true, false, false},
		{"portal user is exempt", true, false, true, false},
		{"platform-owner tenant (flag always off)", false, true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, singleSessionApplies(tt.enforced, tt.platformAdmin, tt.portal))
		})
	}
}

func TestShouldPromptTakeover(t *testing.T) {
	now := time.Now()
	recent := &tenancy.AuthSession{ID: "other", LastSeenAt: now.Add(-time.Minute)}
	edge := &tenancy.AuthSession{ID: "other", LastSeenAt: now.Add(-sessionPromptWindow)}
	stale := &tenancy.AuthSession{ID: "other", LastSeenAt: now.Add(-sessionPromptWindow - time.Second)}
	tests := []struct {
		name  string
		other *tenancy.AuthSession
		want  bool
	}{
		{"no other session", nil, false},
		{"other browser in use", recent, true},
		{"seen exactly at the window", edge, true},
		{"idle past the window: replaced silently", stale, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldPromptTakeover(tt.other, now))
		})
	}
}

func TestCooldownMessage(t *testing.T) {
	tests := []struct {
		wait time.Duration
		want string
	}{
		{30 * time.Second, "Your account was just signed in on another device. Try again in 1 minute."},
		{90 * time.Second, "Your account was just signed in on another device. Try again in 2 minutes."},
		{5 * time.Minute, "Your account was just signed in on another device. Try again in 5 minutes."},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, cooldownMessage(tt.wait))
	}
}

func TestSessionPolicyRequestValidate(t *testing.T) {
	b := func(v bool) *bool { return &v }
	n := func(v int) *int { return &v }
	tests := []struct {
		name string
		req  sessionPolicyRequest
		ok   bool
	}{
		{"empty", sessionPolicyRequest{}, false},
		{"switch only", sessionPolicyRequest{Enforced: b(false)}, true},
		{"numbers at the bounds", sessionPolicyRequest{TakeoverCooldownMinutes: n(0), TakeoverDailyCap: n(100)}, true},
		{"max cooldown", sessionPolicyRequest{TakeoverCooldownMinutes: n(1440)}, true},
		{"cooldown too long", sessionPolicyRequest{TakeoverCooldownMinutes: n(1441)}, false},
		{"negative cooldown", sessionPolicyRequest{TakeoverCooldownMinutes: n(-1)}, false},
		{"cap too high", sessionPolicyRequest{TakeoverDailyCap: n(101)}, false},
		{"negative cap", sessionPolicyRequest{TakeoverDailyCap: n(-1)}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.ok, tt.req.validate() == "")
		})
	}
}

func TestDeviceLabel(t *testing.T) {
	tests := []struct {
		name, ua, want string
	}{
		{"chrome windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36", "Chrome on Windows"},
		{"edge windows", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 Edg/130.0.0.0", "Edge on Windows"},
		{"safari mac", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15", "Safari on macOS"},
		{"firefox linux", "Mozilla/5.0 (X11; Linux x86_64; rv:131.0) Gecko/20100101 Firefox/131.0", "Firefox on Linux"},
		{"chrome iphone", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/130.0 Mobile/15E148 Safari/604.1", "Chrome on iPhone"},
		{"chrome android", "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36", "Chrome on Android"},
		{"opera", "Mozilla/5.0 (Windows NT 10.0) AppleWebKit/537.36 Chrome/130.0 Safari/537.36 OPR/115.0", "Opera on Windows"},
		{"os only", "SomeBot (Windows)", "Windows"},
		{"empty", "", unknownDevice},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, deviceLabel(tt.ua))
		})
	}
}

func TestTruncateUserAgent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"short kept", "Chrome", len("Chrome")},
		{"at cap kept", strings.Repeat("a", maxUserAgentLen), maxUserAgentLen},
		{"long capped", strings.Repeat("a", maxUserAgentLen+10), maxUserAgentLen},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Len(t, truncateUserAgent(tt.in), tt.want)
		})
	}
}

func TestSessionStatusResponse(t *testing.T) {
	now := time.Now()
	replaced, ends := now.Add(-5*time.Second), now.Add(40*time.Second)
	by := &tenancy.AuthSession{ID: "new", DeviceLabel: "Edge on Windows"}
	tests := []struct {
		name       string
		s          *tenancy.AuthSession
		by         *tenancy.AuthSession
		wantStatus string
		wantEnds   bool
		wantDevice any
	}{
		{"live", &tenancy.AuthSession{ID: "a"}, nil, tenancy.SessionActive, false, nil},
		{"replacing", &tenancy.AuthSession{ID: "a", ReplacedAt: &replaced, EndsAt: &ends, ReplacedBy: "new"}, by,
			tenancy.SessionReplacing, true, "Edge on Windows"},
		{"replacing, replacer unknown", &tenancy.AuthSession{ID: "a", ReplacedAt: &replaced, EndsAt: &ends}, nil,
			tenancy.SessionReplacing, true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := sessionStatusResponse(tt.s, tt.by, now)
			assert.Equal(t, true, out["success"])
			assert.Equal(t, tt.wantStatus, out["status"])
			_, hasEnds := out["endsAt"]
			assert.Equal(t, tt.wantEnds, hasEnds)
			if tt.wantDevice == nil {
				assert.NotContains(t, out, "replacedBy")
				return
			}
			rb, ok := out["replacedBy"].(map[string]any)
			assert.True(t, ok)
			assert.Equal(t, tt.wantDevice, rb["deviceLabel"])
		})
	}
}
