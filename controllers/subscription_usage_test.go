package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/usage"
)

type fakeTokenUsage struct {
	anchor    time.Time
	anchorErr error
	totals    usage.TokenTotals
	sumErr    error
	gotTenant string
	gotPeriod usage.Period
}

func (f *fakeTokenUsage) BillingAnchor(_ context.Context, tenantID string) (time.Time, error) {
	f.gotTenant = tenantID
	return f.anchor, f.anchorErr
}

func (f *fakeTokenUsage) SumTokens(_ context.Context, _ string, p usage.Period) (usage.TokenTotals, error) {
	f.gotPeriod = p
	return f.totals, f.sumErr
}

type fakeSnapshotter struct {
	snap      *usage.StorageSnapshot
	err       error
	gotBucket string
}

func (f *fakeSnapshotter) Snapshot(_ context.Context, _, bucket string) (*usage.StorageSnapshot, error) {
	f.gotBucket = bucket
	return f.snap, f.err
}

var usageTestNow = time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)

func usageOps(tokens *fakeTokenUsage, snaps *fakeSnapshotter, allowed bool) *SubscriptionUsageOps {
	return &SubscriptionUsageOps{tokens: tokens, storage: snaps, now: func() time.Time { return usageTestNow },
		resolve: func(*http.Request) (subscriptionUsageEnv, error) {
			return subscriptionUsageEnv{tenantID: "tenant-1", bucket: "ss-acme",
				check: func(context.Context, string) (authz.Decision, error) {
					return authz.Decision{Allowed: allowed, Scope: authz.ScopeAll}, nil
				}}, nil
		}}
}

func usageReq(authed bool) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/subscription/usage", nil)
	if authed {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: "ident-1"}))
	}
	return req
}

func TestSubscriptionUsage_StatusCodes(t *testing.T) {
	anchor := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		authed  bool
		allowed bool
		tokens  *fakeTokenUsage
		snaps   *fakeSnapshotter
		want    int
	}{
		{"unauthenticated", false, true, &fakeTokenUsage{anchor: anchor}, &fakeSnapshotter{}, http.StatusUnauthorized},
		{"no subscription:read", true, false, &fakeTokenUsage{anchor: anchor}, &fakeSnapshotter{}, http.StatusForbidden},
		{"anchor read fails", true, true, &fakeTokenUsage{anchorErr: errors.New("cp down")}, &fakeSnapshotter{}, http.StatusInternalServerError},
		{"token sum fails", true, true, &fakeTokenUsage{anchor: anchor, sumErr: errors.New("cp down")}, &fakeSnapshotter{}, http.StatusInternalServerError},
		{"storage failure degrades, still 200", true, true, &fakeTokenUsage{anchor: anchor}, &fakeSnapshotter{err: errors.New("boom")}, http.StatusOK},
		{"ok", true, true, &fakeTokenUsage{anchor: anchor}, &fakeSnapshotter{}, http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			usageOps(tc.tokens, tc.snaps, tc.allowed).GetUsage(rec, usageReq(tc.authed))
			assert.Equal(t, tc.want, rec.Code)
			if tc.want != http.StatusOK {
				assert.Contains(t, rec.Body.String(), `"success":false`)
			}
		})
	}
}

func TestSubscriptionUsage_Payload(t *testing.T) {
	measured := time.Date(2026, 11, 16, 8, 0, 0, 0, time.UTC)
	tokens := &fakeTokenUsage{
		anchor: time.Date(2025, 3, 20, 0, 0, 0, 0, time.UTC),
		totals: usage.TokenTotals{PromptTokens: 900, CompletionTokens: 300, BySource: []usage.SourceTokens{
			{Source: usage.SourceAsk, PromptTokens: 800, CompletionTokens: 250},
			{Source: usage.SourceImport, PromptTokens: 100, CompletionTokens: 50},
		}},
	}
	snaps := &fakeSnapshotter{snap: &usage.StorageSnapshot{UsedBytes: 4096, ObjectCount: 3, MeasuredAt: measured}}
	rec := httptest.NewRecorder()
	usageOps(tokens, snaps, true).GetUsage(rec, usageReq(true))
	require.Equal(t, http.StatusOK, rec.Code)

	assert.Equal(t, "tenant-1", tokens.gotTenant, "usage is read for the resolved tenant only")
	assert.Equal(t, "ss-acme", snaps.gotBucket, "storage is measured on the tenant's own bucket only")
	assert.JSONEq(t, `{"success":true,"usage":{
		"storage":{"usedBytes":4096,"objectCount":3,"measuredAt":"2026-11-16T08:00:00Z","limitBytes":null},
		"aiTokens":{"periodStart":"2026-10-20","periodEnd":"2026-11-20","promptTokens":900,"completionTokens":300,"totalTokens":1200,"limitTokens":null,
			"bySource":[{"source":"ask","totalTokens":1050},{"source":"import","totalTokens":150}]}}}`,
		rec.Body.String())
}

func TestSubscriptionUsage_NeverMeasuredAndNoTokens(t *testing.T) {
	rec := httptest.NewRecorder()
	usageOps(&fakeTokenUsage{anchor: usageTestNow, totals: usage.TokenTotals{}}, &fakeSnapshotter{}, true).
		GetUsage(rec, usageReq(true))
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		Usage subscriptionUsageJSON `json:"usage"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body.Usage.Storage.MeasuredAt)
	assert.Equal(t, []sourceTokensJSON{}, body.Usage.AITokens.BySource, "bySource is [] not null")
}
