package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/proposedaction"
	"stonesuite-backend/workflow"
)

type fakeActionStore struct {
	err      error
	replayed bool
	caller   proposedaction.Caller
}

func (f *fakeActionStore) action() proposedaction.Action {
	return proposedaction.Action{ID: "a1", Payload: json.RawMessage(`{"secret":"x"}`), Status: proposedaction.StatusPending}
}

func (f *fakeActionStore) List(context.Context, string, int, string) ([]proposedaction.Action, string, error) {
	if f.err != nil {
		return nil, "", f.err
	}
	return []proposedaction.Action{f.action()}, "next", nil
}

func (f *fakeActionStore) Get(context.Context, string, string) (proposedaction.Action, error) {
	return f.action(), f.err
}

func (f *fakeActionStore) Cancel(context.Context, string, string) (proposedaction.Action, error) {
	return f.action(), f.err
}

func (f *fakeActionStore) Confirm(_ context.Context, c proposedaction.Caller, _ string) (proposedaction.Action, bool, error) {
	f.caller = c
	return f.action(), f.replayed, f.err
}

func actionOps(store *fakeActionStore, userErr error, userID string) *ProposedActionOps {
	return &ProposedActionOps{resolve: func(*http.Request) (proposedActionEnv, error) {
		return proposedActionEnv{store: store, userID: func(context.Context, string) (string, error) {
			return userID, userErr
		}}, nil
	}}
}

func doAction(h http.HandlerFunc, method, target string, authed bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.SetPathValue("id", "a1")
	if authed {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: "ident-1"}))
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestProposedActionErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"missing", &proposedaction.NotFoundError{}, http.StatusNotFound},
		{"mismatch", &proposedaction.NotFoundError{Mismatch: true}, http.StatusNotFound},
		{"expired", proposedaction.ErrExpired, http.StatusGone},
		{"conflict", proposedaction.ErrConflict, http.StatusConflict},
		{"forbidden", &proposedaction.ForbiddenError{Resource: authz.ResourceAutomation, Action: authz.ActionUpdate}, http.StatusForbidden},
		{"validation", &proposedaction.ValidationError{Message: "bad qty"}, http.StatusUnprocessableEntity},
		{"execution failed", proposedaction.ErrExecutionFailed, http.StatusUnprocessableEntity},
		{"cursor", proposedaction.ErrInvalidCursor, http.StatusBadRequest},
		{"internal", errors.New("db down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ops := actionOps(&fakeActionStore{err: tt.err}, nil, "u1")
			for name, h := range map[string]http.HandlerFunc{"get": ops.Get, "cancel": ops.Cancel, "confirm": ops.Confirm, "list": ops.List} {
				rec := doAction(h, http.MethodGet, "/api/tenant/actions", true)
				assert.Equal(t, tt.want, rec.Code, name)
				var body map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, false, body["success"], name)
				assert.NotEmpty(t, body["message"], name)
			}
		})
	}
}

func TestProposedActionScopeDenialIndistinguishableFromUnknown(t *testing.T) {
	unknown := doAction(actionOps(&fakeActionStore{err: &proposedaction.NotFoundError{}}, nil, "u1").Confirm, http.MethodPost, "/x", true)
	scopeDenied := doAction(actionOps(&fakeActionStore{err: &proposedaction.NotFoundError{Mismatch: true}}, nil, "u1").Confirm, http.MethodPost, "/x", true)
	assert.Equal(t, http.StatusNotFound, scopeDenied.Code)
	assert.Equal(t, unknown.Code, scopeDenied.Code)
	assert.Equal(t, unknown.Body.String(), scopeDenied.Body.String())
}

func TestProposedActionValidationMessage(t *testing.T) {
	ops := actionOps(&fakeActionStore{err: &proposedaction.ValidationError{Message: "bad qty"}}, nil, "u1")
	rec := doAction(ops.Confirm, http.MethodPost, "/x", true)
	assert.Contains(t, rec.Body.String(), "bad qty")
}

func TestProposedActionUnauthenticated(t *testing.T) {
	ops := actionOps(&fakeActionStore{}, nil, "u1")
	for name, h := range map[string]http.HandlerFunc{"get": ops.Get, "cancel": ops.Cancel, "confirm": ops.Confirm, "list": ops.List} {
		assert.Equal(t, http.StatusUnauthorized, doAction(h, http.MethodGet, "/x", false).Code, name)
	}
}

func TestProposedActionSuccessNoPayload(t *testing.T) {
	ops := actionOps(&fakeActionStore{}, nil, "u1")
	for name, h := range map[string]http.HandlerFunc{"get": ops.Get, "cancel": ops.Cancel, "confirm": ops.Confirm, "list": ops.List} {
		rec := doAction(h, http.MethodGet, "/x", true)
		assert.Equal(t, http.StatusOK, rec.Code, name)
		assert.NotContains(t, rec.Body.String(), "payload", name)
		assert.NotContains(t, rec.Body.String(), "secret", name)
	}
}

func TestProposedActionConfirm(t *testing.T) {
	tests := []struct {
		name     string
		replayed bool
		userErr  error
		userID   string
		want     int
	}{
		{"fresh", false, nil, "u1", http.StatusOK},
		{"replay", true, nil, "u1", http.StatusOK},
		{"no profile", false, workflow.ErrNoTenantUser, "", http.StatusForbidden},
		{"empty user", false, nil, "", http.StatusForbidden},
		{"lookup failure", false, errors.New("db"), "", http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeActionStore{replayed: tt.replayed}
			rec := doAction(actionOps(store, tt.userErr, tt.userID).Confirm, http.MethodPost, "/x", true)
			assert.Equal(t, tt.want, rec.Code)
			if tt.want == http.StatusOK {
				var body map[string]any
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
				assert.Equal(t, tt.replayed, body["replayed"])
				assert.Equal(t, proposedaction.Caller{IdentityID: "ident-1", UserID: "u1"}, store.caller)
			}
		})
	}
}

func TestProposedActionListLimit(t *testing.T) {
	ops := actionOps(&fakeActionStore{}, nil, "u1")
	tests := []struct {
		query string
		want  int
	}{{"", 200}, {"?limit=10", 200}, {"?limit=abc", 400}, {"?limit=0", 400}, {"?limit=-1", 400}}
	for _, tt := range tests {
		assert.Equal(t, tt.want, doAction(ops.List, http.MethodGet, "/api/tenant/actions"+tt.query, true).Code, tt.query)
	}
}
