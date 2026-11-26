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

	"stonesuite-backend/middleware"
	"stonesuite-backend/usage"
)

type fakeRoller struct {
	calls int
	day   time.Time
	sum   usage.RollupSummary
	err   error
}

func (f *fakeRoller) Run(_ context.Context, day time.Time) (usage.RollupSummary, error) {
	f.calls++
	f.day = day
	return f.sum, f.err
}

const (
	usageRollupTestSecret = "usage-rollup-test-secret-not-a-real-key"
	usageRollupTestPath   = "/api/internal/usage/rollup"
)

func newUsageRollupHTTPRequest(ts, sig, body string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, usageRollupTestPath, strings.NewReader(body))
	if ts != "" {
		req.Header.Set(middleware.InternalTimestampHeader, ts)
	}
	if sig != "" {
		req.Header.Set(middleware.InternalSignatureHeader, sig)
	}
	return req
}

func TestUsageRollup(t *testing.T) {
	now := time.Date(2026, 11, 26, 2, 41, 0, 0, time.UTC)
	ts := strconv.FormatInt(now.Unix(), 10)
	sign := func(secret, body string) string {
		return middleware.SignInternal(secret, http.MethodPost, usageRollupTestPath, ts, []byte(body))
	}
	otherSecret := "some-other-test-secret-not-a-real-key"

	tests := []struct {
		name      string
		secret    string
		body      string
		signWith  string
		rollErr   error
		wantCode  int
		wantCalls int
		wantDay   string
	}{
		{"unset secret is 404", "", "{}", usageRollupTestSecret, nil, http.StatusNotFound, 0, ""},
		{"bad signature is 401", usageRollupTestSecret, "{}", otherSecret, nil, http.StatusUnauthorized, 0, ""},
		{"default day is yesterday UTC", usageRollupTestSecret, "{}", usageRollupTestSecret, nil, http.StatusOK, 1, "2026-11-25"},
		{"empty body defaults to yesterday", usageRollupTestSecret, "", usageRollupTestSecret, nil, http.StatusOK, 1, "2026-11-25"},
		{"explicit valid day passes through", usageRollupTestSecret, `{"day":"2026-11-10"}`, usageRollupTestSecret, nil, http.StatusOK, 1, "2026-11-10"},
		{"oldest allowed day passes", usageRollupTestSecret, `{"day":"2026-10-26"}`, usageRollupTestSecret, nil, http.StatusOK, 1, "2026-10-26"},
		{"invalid day is 400", usageRollupTestSecret, `{"day":"yesterday"}`, usageRollupTestSecret, nil, http.StatusBadRequest, 0, ""},
		{"malformed json is 400", usageRollupTestSecret, `{"day":`, usageRollupTestSecret, nil, http.StatusBadRequest, 0, ""},
		{"today is 400", usageRollupTestSecret, `{"day":"2026-11-26"}`, usageRollupTestSecret, nil, http.StatusBadRequest, 0, ""},
		{"future day is 400", usageRollupTestSecret, `{"day":"2026-11-27"}`, usageRollupTestSecret, nil, http.StatusBadRequest, 0, ""},
		{"older than 31 days is 400", usageRollupTestSecret, `{"day":"2026-10-25"}`, usageRollupTestSecret, nil, http.StatusBadRequest, 0, ""},
		{"roller error is 500", usageRollupTestSecret, "{}", usageRollupTestSecret, errors.New("cp down"), http.StatusInternalServerError, 1, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fr := &fakeRoller{sum: usage.RollupSummary{Tenants: 4, Failed: 1}, err: tc.rollErr}
			h := NewUsageRollupOps(fr, tc.secret)
			h.now = func() time.Time { return now }

			rec := httptest.NewRecorder()
			h.Rollup(rec, newUsageRollupHTTPRequest(ts, sign(tc.signWith, tc.body), tc.body))

			assert.Equal(t, tc.wantCode, rec.Code)
			assert.Equal(t, tc.wantCalls, fr.calls)
			if tc.wantCode == http.StatusOK {
				assert.Equal(t, tc.wantDay, fr.day.Format("2006-01-02"))
				assert.JSONEq(t, `{"success":true,"day":"`+tc.wantDay+`","summary":{"tenants":4,"failed":1}}`, rec.Body.String())
			} else {
				assert.Contains(t, rec.Body.String(), `"success":false`)
			}
		})
	}
}

func TestUsageRollup_ReplayRejected(t *testing.T) {
	now := time.Date(2026, 11, 26, 2, 41, 0, 0, time.UTC)
	ts := strconv.FormatInt(now.Unix(), 10)
	sig := middleware.SignInternal(usageRollupTestSecret, http.MethodPost, usageRollupTestPath, ts, []byte("{}"))
	fr := &fakeRoller{}
	h := NewUsageRollupOps(fr, usageRollupTestSecret)
	h.now = func() time.Time { return now }

	first := httptest.NewRecorder()
	h.Rollup(first, newUsageRollupHTTPRequest(ts, sig, "{}"))
	replay := httptest.NewRecorder()
	h.Rollup(replay, newUsageRollupHTTPRequest(ts, sig, "{}"))

	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, http.StatusUnauthorized, replay.Code)
	assert.Equal(t, 1, fr.calls, "a replayed signature must not run the rollup again")
}
