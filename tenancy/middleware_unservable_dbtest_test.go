//go:build dbtest

package tenancy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
)

// The staff tenant middleware already refuses an unservable workspace with a 403;
// this pins that the body also carries a stable machine-readable code, which
// is what lets the web app end the session with a clear message instead of
// leaving a signed-in user on a dashboard whose every request fails. (The
// customer middleware makes the identical writeUnservable call.)
func TestResolverMiddlewares_UnservableTenantResponseCarriesCode_DB(t *testing.T) {
	cp := newCPTestControlPlane(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		prepare  func(t *testing.T, tenantID string)
		wantCode string
		wantSub  string
	}{
		{"suspended", func(t *testing.T, id string) {
			require.NoError(t, cp.SetTenantStatus(ctx, id, StatusSuspended))
		}, models.CodeWorkspaceSuspended, "suspended"},
		{"deleted", func(t *testing.T, id string) {
			require.NoError(t, cp.MarkTenantDeleted(ctx, id, time.Now()))
		}, models.CodeWorkspaceDeleted, "deleted"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tenantID := seedTestTenant(t, cp)
			tc.prepare(t, tenantID)
			called := false
			next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })
			handler := NewResolver(cp, NewRouter(nil)).Middleware(next)

			req := httptest.NewRequest(http.MethodGet, "/api/tenant/anything", nil)
			req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
				middleware.UserContextPayload{ID: "identity-1", TenantID: tenantID}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.False(t, called, "the wrapped handler must not run")
			var body models.APIResponse
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
			assert.Equal(t, tc.wantCode, body.Code)
			assert.Contains(t, body.Message, tc.wantSub)
		})
	}
}
