package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPlatformUsage_NonAdminForbidden(t *testing.T) {
	ops, _ := newPlatUsageOps(false)
	tests := []struct {
		name string
		call func(http.ResponseWriter, *http.Request)
		req  *http.Request
	}{
		{"overview", ops.Overview, platUsageReq("GET", "/api/platform/usage/overview", "", "", "")},
		{"watchlist", ops.Watchlist, platUsageReq("GET", "/api/platform/usage/watchlist", "", "", "")},
		{"detail", ops.TenantDetail, platUsageReq("GET", "/x", "", "tenantId", platUsageTenantID)},
		{"ack", ops.Ack, platUsageReq("POST", "/x", "{}", "alertId", platUsageAlertID)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.call(rec, tc.req)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Equal(t, false, decodePlatUsage(t, rec)["success"])
		})
	}
}

func TestPlatformUsage_BadParams(t *testing.T) {
	ops, _ := newPlatUsageOps(true)
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{"bad period", "/api/platform/usage/overview?period=14d", http.StatusBadRequest},
		{"limit zero", "/api/platform/usage/overview?limit=0", http.StatusBadRequest},
		{"limit too big", "/api/platform/usage/overview?limit=201", http.StatusBadRequest},
		{"limit not a number", "/api/platform/usage/overview?limit=abc", http.StatusBadRequest},
		{"bad cursor", "/api/platform/usage/overview?cursor=bad", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ops.Overview(rec, platUsageReq("GET", tc.target, "", "", ""))
			assert.Equal(t, tc.want, rec.Code)
			assert.Equal(t, false, decodePlatUsage(t, rec)["success"])
		})
	}
	rec := httptest.NewRecorder()
	ops.TenantDetail(rec, platUsageReq("GET", "/x?period=nope", "", "tenantId", platUsageTenantID))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestPlatformUsage_UnknownTenant404(t *testing.T) {
	ops, _ := newPlatUsageOps(true)
	for _, id := range []string{"33333333-3333-4333-8333-333333333333", "not-a-uuid"} {
		rec := httptest.NewRecorder()
		ops.TenantDetail(rec, platUsageReq("GET", "/x", "", "tenantId", id))
		assert.Equal(t, http.StatusNotFound, rec.Code)
		assert.Equal(t, "Usage is not available for this tenant.", decodePlatUsage(t, rec)["message"])
	}
}

func TestPlatformUsage_Ack(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		alertID    string
		found      bool
		want       int
		wantSnooze int
	}{
		{"plain ack", `{}`, platUsageAlertID, true, http.StatusOK, 0},
		{"empty body", ``, platUsageAlertID, true, http.StatusOK, 0},
		{"snooze 7", `{"snoozeDays":7}`, platUsageAlertID, true, http.StatusOK, 7},
		{"snooze 30", `{"snoozeDays":30}`, platUsageAlertID, true, http.StatusOK, 30},
		{"snooze 14", `{"snoozeDays":14}`, platUsageAlertID, true, http.StatusBadRequest, 0},
		{"snooze zero", `{"snoozeDays":0}`, platUsageAlertID, true, http.StatusBadRequest, 0},
		{"invalid json", `{`, platUsageAlertID, true, http.StatusBadRequest, 0},
		{"unknown field", `{"foo":1}`, platUsageAlertID, true, http.StatusBadRequest, 0},
		{"unknown alert", `{}`, platUsageAlertID, false, http.StatusNotFound, 0},
		{"malformed alert id", `{}`, "nope", true, http.StatusNotFound, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ops, st := newPlatUsageOps(true)
			st.ackFound = tc.found
			rec := httptest.NewRecorder()
			ops.Ack(rec, platUsageReq("POST", "/x", tc.body, "alertId", tc.alertID))
			require.Equal(t, tc.want, rec.Code)
			body := decodePlatUsage(t, rec)
			if tc.want == http.StatusOK {
				assert.Equal(t, true, body["success"])
				assert.Equal(t, "identity-1", st.ackActor)
				assert.Equal(t, tc.wantSnooze, st.ackSnooze)
			} else {
				assert.Equal(t, false, body["success"])
				assert.NotEmpty(t, body["message"])
			}
		})
	}
}
