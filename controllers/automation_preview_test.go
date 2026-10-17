package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
)

type fakePreviewer struct{}

func (fakePreviewer) Preview(_ context.Context, key string) (automation.Preview, error) {
	switch key {
	case "ar_dunning":
		return automation.Preview{
			Counts: map[string]int{"planned": 1},
			Recipients: []automation.PreviewRecipient{{Email: "a@x.com",
				Items: []automation.PlanRow{{InvoiceUUID: "u", Number: "INV-1", Email: "a@x.com", Status: "planned"}}}},
		}, nil
	case "other":
		return automation.Preview{}, automation.ErrPreviewUnsupported
	}
	return automation.Preview{}, automation.ErrRuleNotFound
}

func postPreview(allowed map[authz.Action]bool, authed bool, key string) *httptest.ResponseRecorder {
	h := &AutomationPreviewOps{resolve: func(*http.Request) (automationPreviewEnv, error) {
		return automationPreviewEnv{previewer: fakePreviewer{}, check: func(_ context.Context, _ string, a authz.Action) (authz.Decision, error) {
			return authz.Decision{Allowed: allowed[a], Scope: authz.ScopeAll}, nil
		}}, nil
	}}
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/automation/rules/"+key+"/preview", nil)
	req.SetPathValue("key", key)
	if authed {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: "ident-1"}))
	}
	rec := httptest.NewRecorder()
	h.Preview(rec, req)
	return rec
}

func TestAutomationPreview(t *testing.T) {
	read := map[authz.Action]bool{authz.ActionRead: true}
	tests := []struct {
		name    string
		allowed map[authz.Action]bool
		authed  bool
		key     string
		want    int
	}{
		{"unauthenticated", read, false, "ar_dunning", http.StatusUnauthorized},
		{"no read", nil, true, "ar_dunning", http.StatusForbidden},
		{"unknown key", read, true, "nope", http.StatusNotFound},
		{"unsupported key", read, true, "other", http.StatusBadRequest},
		{"ok", read, true, "ar_dunning", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postPreview(tt.allowed, tt.authed, tt.key)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			if tt.want == http.StatusOK {
				var body struct {
					Success    bool           `json:"success"`
					Counts     map[string]int `json:"counts"`
					Recipients []struct {
						Email string `json:"email"`
					} `json:"recipients"`
				}
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.True(t, body.Success)
				assert.Equal(t, 1, body.Counts["planned"])
				require.Len(t, body.Recipients, 1)
				assert.Equal(t, "a@x.com", body.Recipients[0].Email)
			}
		})
	}
}
