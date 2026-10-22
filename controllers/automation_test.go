package controllers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
)

type fakeTicker struct {
	calls int
	sum   automation.Summary
	err   error
}

func (f *fakeTicker) Tick(context.Context, time.Time) (automation.Summary, error) {
	f.calls++
	return f.sum, f.err
}

const (
	tickTestSecret = "automation-tick-test-secret-not-a-real-key"
	tickPath       = "/api/internal/automation/tick"
	tickBody       = "{}"
)

func tickRequest(ts, sig string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, tickPath, strings.NewReader(tickBody))
	if ts != "" {
		req.Header.Set(middleware.InternalTimestampHeader, ts)
	}
	if sig != "" {
		req.Header.Set(middleware.InternalSignatureHeader, sig)
	}
	return req
}

func TestAutomationTick(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	good := middleware.SignInternal(tickTestSecret, http.MethodPost, tickPath, ts, []byte(tickBody))

	tests := []struct {
		name      string
		secret    string
		sig       string
		ts        string
		tickErr   error
		wantCode  int
		wantCalls int
	}{
		{"disabled when secret unset", "", good, ts, nil, http.StatusNotFound, 0},
		{"valid signature runs tick", tickTestSecret, good, ts, nil, http.StatusOK, 1},
		{"bad signature rejected", tickTestSecret, middleware.SignInternal("some-other-test-secret-not-a-real-key", http.MethodPost, tickPath, ts, []byte(tickBody)), ts, nil, http.StatusUnauthorized, 0},
		{"signature for another route rejected", tickTestSecret, middleware.SignInternal(tickTestSecret, http.MethodPost, "/api/internal/other", ts, []byte(tickBody)), ts, nil, http.StatusUnauthorized, 0},
		{"missing headers rejected", tickTestSecret, "", "", nil, http.StatusUnauthorized, 0},
		{"tick failure is 500", tickTestSecret, good, ts, errors.New("cp down"), http.StatusInternalServerError, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ft := &fakeTicker{sum: automation.Summary{Tenants: 2, Transitioned: 3}, err: tc.tickErr}
			h := NewAutomationOps(ft, fakeGate{on: true}, nil, tc.secret)
			h.now = func() time.Time { return now }

			rec := httptest.NewRecorder()
			h.Tick(rec, tickRequest(tc.ts, tc.sig))

			assert.Equal(t, tc.wantCode, rec.Code)
			assert.Equal(t, tc.wantCalls, ft.calls)
			if tc.wantCode == http.StatusOK {
				assert.JSONEq(t, `{"success":true,"tenants":2,"busy":0,"transitioned":3,"skipped":0,"failed":0,"sent":0,"failed_sends":0}`, rec.Body.String())
			} else {
				assert.Contains(t, rec.Body.String(), `"success":false`)
			}
		})
	}
}

func TestAutomationTick_ReplayRejected(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := middleware.SignInternal(tickTestSecret, http.MethodPost, tickPath, ts, []byte(tickBody))
	ft := &fakeTicker{}
	h := NewAutomationOps(ft, fakeGate{on: true}, nil, tickTestSecret)
	h.now = func() time.Time { return now }

	first := httptest.NewRecorder()
	h.Tick(first, tickRequest(ts, sig))
	replay := httptest.NewRecorder()
	h.Tick(replay, tickRequest(ts, sig))

	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusUnauthorized, replay.Code)
	assert.Equal(t, 1, ft.calls, "a replayed signature must not run the tick again")
}

type fakeGate struct {
	on  bool
	err error
}

func (f fakeGate) Enabled(context.Context) (bool, error) { return f.on, f.err }

func TestAutomationTick_PlatformGate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := middleware.SignInternal(tickTestSecret, http.MethodPost, tickPath, ts, []byte(tickBody))

	tests := []struct {
		name      string
		gate      fakeGate
		wantCode  int
		wantCalls int
		wantBody  string
	}{
		{"enabled runs the tick", fakeGate{on: true}, http.StatusOK, 1, `"success":true`},
		{"disabled skips without ticking", fakeGate{on: false}, http.StatusOK, 0, `"skipped":"platform_disabled"`},
		{"unreadable switch fails closed", fakeGate{err: errors.New("db down")}, http.StatusServiceUnavailable, 0, `"success":false`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ft := &fakeTicker{}
			h := NewAutomationOps(ft, tt.gate, nil, tickTestSecret)
			h.now = func() time.Time { return now }
			w := httptest.NewRecorder()

			h.Tick(w, tickRequest(ts, sig))

			assert.Equal(t, tt.wantCode, w.Code)
			assert.Equal(t, tt.wantCalls, ft.calls)
			assert.Contains(t, w.Body.String(), tt.wantBody)
		})
	}
}

// A bad signature must be rejected before the gate is consulted, so an
// unauthenticated caller can't use the endpoint to probe the switch.
func TestAutomationTick_BadSignatureBeforeGate(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	h := NewAutomationOps(&fakeTicker{}, fakeGate{on: false}, nil, tickTestSecret)
	h.now = func() time.Time { return now }
	w := httptest.NewRecorder()

	h.Tick(w, tickRequest(ts, "deadbeef"))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.NotContains(t, w.Body.String(), "platform_disabled")
}

func TestAutomationTick_NilGateFailsClosed(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := middleware.SignInternal(tickTestSecret, http.MethodPost, tickPath, ts, []byte(tickBody))
	ft := &fakeTicker{}
	h := NewAutomationOps(ft, nil, nil, tickTestSecret)
	h.now = func() time.Time { return now }
	w := httptest.NewRecorder()

	h.Tick(w, tickRequest(ts, sig))

	assert.Equal(t, 0, ft.calls)
	assert.Contains(t, w.Body.String(), "platform_disabled")
}

func TestAutomationTick_SelfHealsStaleKV(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	tests := []struct {
		name      string
		gate      fakeGate
		mirror    *fakeAutomationMirror
		wantCalls int
	}{
		{"disabled + configured mirror resets KV to off", fakeGate{on: false}, &fakeAutomationMirror{configured: true}, 1},
		{"disabled + KV write fails: still skipped, no panic", fakeGate{on: false}, &fakeAutomationMirror{configured: true, err: errors.New("cf down")}, 1},
		{"disabled + unconfigured mirror: no call", fakeGate{on: false}, &fakeAutomationMirror{}, 0},
		{"enabled: KV untouched", fakeGate{on: true}, &fakeAutomationMirror{configured: true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sig := middleware.SignInternal(tickTestSecret, http.MethodPost, tickPath, ts, []byte(tickBody))
			h := NewAutomationOps(&fakeTicker{}, tt.gate, tt.mirror, tickTestSecret)
			h.now = func() time.Time { return now }
			w := httptest.NewRecorder()

			h.Tick(w, tickRequest(ts, sig))

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tt.wantCalls, tt.mirror.calls)
			if tt.wantCalls > 0 {
				assert.False(t, tt.mirror.last, "heal must only ever write off")
			}
		})
	}
}
