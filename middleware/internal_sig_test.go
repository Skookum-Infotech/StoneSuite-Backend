package middleware

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestVerifyInternalSignature(t *testing.T) {
	const secret = "automation-tick-test-secret-not-a-real-key"
	const m, p = "POST", "/api/internal/automation/tick"
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{}`)
	good := SignInternal(secret, m, p, ts, body)

	tests := []struct {
		name   string
		secret string
		path   string
		ts     string
		sig    string
		body   []byte
		now    time.Time
		want   error
	}{
		{"valid", secret, p, ts, good, body, now, nil},
		{"valid at edge of skew", secret, p, ts, good, body, now.Add(InternalSignatureMaxSkew), nil},
		{"empty secret never verifies", "", p, ts, SignInternal("", m, p, ts, body), body, now, ErrInternalSigMissing},
		{"missing timestamp", secret, p, "", good, body, now, ErrInternalSigMissing},
		{"missing signature", secret, p, ts, "", body, now, ErrInternalSigMissing},
		{"non-numeric timestamp", secret, p, "yesterday", good, body, now, ErrInternalSigMissing},
		{"stale (replay)", secret, p, ts, good, body, now.Add(InternalSignatureMaxSkew + time.Second), ErrInternalSigStale},
		{"from the future", secret, p, ts, good, body, now.Add(-InternalSignatureMaxSkew - time.Second), ErrInternalSigStale},
		{"tampered body", secret, p, ts, good, []byte(`{"x":1}`), now, ErrInternalSigInvalid},
		{"signature for a different route", secret, "/api/internal/other", ts, good, body, now, ErrInternalSigInvalid},
		{"wrong secret", secret, p, ts, SignInternal("some-other-test-secret-not-a-real-key", m, p, ts, body), body, now, ErrInternalSigInvalid},
		{"non-hex signature", secret, p, ts, "zz", body, now, ErrInternalSigInvalid},
		{"signature over different timestamp", secret, p, ts, SignInternal(secret, m, p, "1799999999", body), body, now, ErrInternalSigInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyInternalSignature(tc.secret, m, tc.path, tc.ts, tc.sig, tc.body, tc.now)
			assert.ErrorIs(t, err, tc.want)
			if tc.want == nil {
				assert.NoError(t, err)
			}
		})
	}
}

func TestReplayGuard(t *testing.T) {
	g := NewReplayGuard()
	now := time.Unix(1_800_000_000, 0)

	assert.True(t, g.FirstUse("a", now), "first use accepted")
	assert.False(t, g.FirstUse("a", now.Add(time.Minute)), "replay inside window rejected")
	assert.True(t, g.FirstUse("b", now), "different signature accepted")
	// Long after the window the entry is pruned (and the timestamp check
	// would reject the request anyway), so memory stays bounded.
	assert.True(t, g.FirstUse("a", now.Add(3*InternalSignatureMaxSkew)))
	assert.Len(t, g.seen, 1)
}
