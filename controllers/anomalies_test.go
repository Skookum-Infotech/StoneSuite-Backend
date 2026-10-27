package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/anomaly"
	"stonesuite-backend/automation"
)

func TestParseAnomalyFilter(t *testing.T) {
	validCursor := anomaly.EncodeCursor(anomaly.Cursor{DetectedAt: "2026-10-27 09:15:30.123456", ID: 9})
	tests := []struct {
		name       string
		rawQuery   string
		wantMsg    string
		wantStatus anomaly.Status
		wantLimit  int
		wantCursor bool
	}{
		{name: "defaults", rawQuery: "", wantLimit: anomaly.DefaultPageSize},
		{name: "status filter", rawQuery: "status=dismissed", wantStatus: anomaly.StatusDismissed, wantLimit: anomaly.DefaultPageSize},
		{name: "unknown status", rawQuery: "status=resolved", wantMsg: "Invalid status."},
		{name: "limit", rawQuery: "limit=10", wantLimit: 10},
		{name: "limit capped", rawQuery: "limit=100000", wantLimit: anomaly.MaxPageSize},
		{name: "limit not a number", rawQuery: "limit=abc", wantMsg: "limit must be a positive integer."},
		{name: "limit zero", rawQuery: "limit=0", wantMsg: "limit must be a positive integer."},
		{name: "cursor", rawQuery: "cursor=" + validCursor, wantLimit: anomaly.DefaultPageSize, wantCursor: true},
		{name: "forged cursor", rawQuery: "cursor=Zm9yZ2Vk", wantMsg: "Invalid cursor."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/tenant/anomalies?"+tt.rawQuery, nil)
			f, msg := parseAnomalyFilter(r)
			assert.Equal(t, tt.wantMsg, msg)
			if tt.wantMsg != "" {
				return
			}
			assert.Equal(t, tt.wantStatus, f.Status)
			assert.Equal(t, tt.wantLimit, f.Limit)
			require.Equal(t, tt.wantCursor, f.Cursor != nil)
		})
	}
}

func TestAnomalyUUIDRE(t *testing.T) {
	assert.True(t, anomalyUUIDRE.MatchString("0f8fad5b-d9cb-469f-a165-70867728950e"))
	for _, bad := range []string{"", "not-a-uuid", "0F8FAD5B-D9CB-469F-A165-70867728950E", "0f8fad5b-d9cb-469f-a165-70867728950e'; --"} {
		assert.False(t, anomalyUUIDRE.MatchString(bad), bad)
	}
}

func TestScanNowOutcome(t *testing.T) {
	tests := []struct {
		name       string
		res        automation.Result
		err        error
		wantStatus int
		wantMsg    string
	}{
		{name: "ok", res: automation.Result{Transitioned: 2, Skipped: 5, Sent: 2}, wantStatus: http.StatusOK},
		{name: "switched off", err: automation.ErrAnomalyScanOff, wantStatus: http.StatusConflict, wantMsg: "switched off"},
		{name: "wrapped off still recognised", err: fmt.Errorf("x: %w", automation.ErrAnomalyScanOff), wantStatus: http.StatusConflict, wantMsg: "switched off"},
		{name: "already running", err: automation.ErrAnomalyScanBusy, wantStatus: http.StatusConflict, wantMsg: "already running"},
		{name: "failure hides the cause", err: errors.New("pq: secret detail"), wantStatus: http.StatusInternalServerError, wantMsg: "did not finish"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := scanNowOutcome(tt.res, tt.err)
			assert.Equal(t, tt.wantStatus, status)
			raw, err := json.Marshal(body)
			require.NoError(t, err)
			if tt.wantMsg != "" {
				assert.Contains(t, string(raw), tt.wantMsg)
				assert.NotContains(t, string(raw), "secret detail")
				return
			}
			assert.JSONEq(t, `{"success":true,"raised":2,"alreadyKnown":5,"alertsSent":2,"alertsFailed":0}`, string(raw))
		})
	}
}
