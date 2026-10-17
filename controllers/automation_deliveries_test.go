package controllers

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
)

type fakeDeliveryStore struct {
	got  DeliveryFilter
	rows int
}

func (f *fakeDeliveryStore) List(_ context.Context, flt DeliveryFilter) ([]AutomationDelivery, string, error) {
	f.got = flt
	out := []AutomationDelivery{}
	for i := 0; i < min(f.rows, flt.Limit); i++ {
		out = append(out, AutomationDelivery{ID: "id", RuleKey: "ar_dunning"})
	}
	next := ""
	if f.rows > flt.Limit {
		next = encodeDeliveryCursor(time.Unix(0, 0), "00000000-0000-0000-0000-000000000001")
	}
	return out, next, nil
}

func deliveryOps(store *fakeDeliveryStore, allowed map[authz.Action]bool) *AutomationDeliveryOps {
	return &AutomationDeliveryOps{resolve: func(*http.Request) (automationDeliveryEnv, error) {
		return automationDeliveryEnv{store: store, check: func(_ context.Context, _ string, a authz.Action) (authz.Decision, error) {
			return authz.Decision{Allowed: allowed[a], Scope: authz.ScopeAll}, nil
		}}, nil
	}}
}

func getDeliveries(h *AutomationDeliveryOps, rawQuery string, authed bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/automation/deliveries?"+rawQuery, nil)
	if authed {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: "ident-1"}))
	}
	rec := httptest.NewRecorder()
	h.List(rec, req)
	return rec
}

func TestAutomationDeliveries_Validation(t *testing.T) {
	read := map[authz.Action]bool{authz.ActionRead: true}
	badCursor := base64.RawURLEncoding.EncodeToString([]byte("nonsense"))
	tests := []struct {
		name    string
		allowed map[authz.Action]bool
		authed  bool
		query   string
		want    int
	}{
		{"unauthenticated", read, false, "", http.StatusUnauthorized},
		{"no read", nil, true, "", http.StatusForbidden},
		{"bogus status", read, true, "status=bogus", http.StatusBadRequest},
		{"limit zero", read, true, "limit=0", http.StatusBadRequest},
		{"limit negative", read, true, "limit=-3", http.StatusBadRequest},
		{"limit non-numeric", read, true, "limit=abc", http.StatusBadRequest},
		{"cursor not base64", read, true, "cursor=%21%21", http.StatusBadRequest},
		{"cursor bad shape", read, true, "cursor=" + badCursor, http.StatusBadRequest},
		{"ruleKey injection", read, true, "ruleKey=x%27--", http.StatusBadRequest},
		{"ok", read, true, "status=sent&ruleKey=ar_dunning", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := getDeliveries(deliveryOps(&fakeDeliveryStore{}, tt.allowed), tt.query, tt.authed)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
		})
	}
}

func TestAutomationDeliveries_Limits(t *testing.T) {
	read := map[authz.Action]bool{authz.ActionRead: true}
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"default", "", 25},
		{"explicit", "limit=7", 7},
		{"clamped", "limit=500", 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeDeliveryStore{}
			require.Equal(t, http.StatusOK, getDeliveries(deliveryOps(store, read), tt.query, true).Code)
			assert.Equal(t, tt.want, store.got.Limit)
		})
	}
}

func TestAutomationDeliveries_NextCursorOnlyWhenMoreRows(t *testing.T) {
	read := map[authz.Action]bool{authz.ActionRead: true}
	more := getDeliveries(deliveryOps(&fakeDeliveryStore{rows: 3}, read), "limit=2", true)
	assert.Contains(t, more.Body.String(), `"nextCursor":"`+encodeDeliveryCursor(time.Unix(0, 0), "00000000-0000-0000-0000-000000000001")+`"`)
	last := getDeliveries(deliveryOps(&fakeDeliveryStore{rows: 2}, read), "limit=2", true)
	assert.Contains(t, last.Body.String(), `"nextCursor":""`)
}

func TestAutomationDeliveries_CursorRoundTrip(t *testing.T) {
	ts := time.Date(2026, 10, 1, 9, 0, 0, 123456000, time.UTC)
	id := "11111111-2222-3333-4444-555555555555"
	cur, err := decodeDeliveryCursor(encodeDeliveryCursor(ts, id))
	require.NoError(t, err)
	assert.True(t, ts.Equal(cur.CreatedAt))
	assert.Equal(t, id, cur.ID)
}
