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

	"stonesuite-backend/approvalinbox"
	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
)

type fakeInboxLister struct {
	called bool
	got    approvalinbox.Params
	caller approvalinbox.Caller
	check  approvalinbox.CheckFunc
	page   approvalinbox.Page
	err    error
}

func (f *fakeInboxLister) List(_ context.Context, check approvalinbox.CheckFunc, c approvalinbox.Caller,
	p approvalinbox.Params, _ time.Time) (approvalinbox.Page, error) {
	f.called, f.got, f.caller, f.check = true, p, c, check
	return f.page, f.err
}

func inboxOps(l *fakeInboxLister, superAdmin bool, check approvalinbox.CheckFunc) *ApprovalInboxOps {
	return &ApprovalInboxOps{
		now: time.Now,
		resolve: func(_ *http.Request, id string) (approvalInboxEnv, error) {
			return approvalInboxEnv{
				caller: approvalinbox.Caller{IdentityID: id, EmployeeID: 7, IsSuperAdmin: superAdmin},
				check:  check, lister: l,
			}, nil
		},
	}
}

func getInbox(h *ApprovalInboxOps, rawQuery string, authed bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/approvals/inbox?"+rawQuery, nil)
	if authed {
		req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
			middleware.UserContextPayload{ID: "ident-1"}))
	}
	rec := httptest.NewRecorder()
	h.List(rec, req)
	return rec
}

func allowAll(authz.Resource, authz.Action) authz.Decision {
	return authz.Decision{Allowed: true, Scope: authz.ScopeAll}
}

func TestApprovalInbox_RequiresAuthAndGet(t *testing.T) {
	l := &fakeInboxLister{}
	assert.Equal(t, http.StatusUnauthorized, getInbox(inboxOps(l, false, allowAll), "", false).Code)

	req := httptest.NewRequest(http.MethodPost, "/api/tenant/approvals/inbox", nil)
	rec := httptest.NewRecorder()
	inboxOps(l, false, allowAll).List(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.False(t, l.called)
}

func TestApprovalInbox_ParamValidation(t *testing.T) {
	badCursor := approvalinbox.Cursor{}.Encode() // zero time, empty key: not a cursor the package would issue
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"defaults", "", http.StatusOK},
		{"scope mine", "scope=mine", http.StatusOK},
		{"unknown scope", "scope=everyone", http.StatusBadRequest},
		{"unknown module", "module=nonsense", http.StatusBadRequest},
		{"registry module", "module=invoice", http.StatusOK},
		{"crm is not an approvalchain module", "module=lead", http.StatusBadRequest},
		{"limit zero", "limit=0", http.StatusBadRequest},
		{"limit text", "limit=abc", http.StatusBadRequest},
		{"limit over max is clamped", "limit=100000", http.StatusOK},
		{"garbage cursor", "cursor=nonsense", http.StatusBadRequest},
		{"cursor with empty module", "cursor=" + badCursor, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := &fakeInboxLister{}
			rec := getInbox(inboxOps(l, false, allowAll), tt.query, true)
			assert.Equal(t, tt.want, rec.Code, rec.Body.String())
			assert.Equal(t, tt.want == http.StatusOK, l.called, "lister must only run for valid requests")
		})
	}
}

func TestApprovalInbox_DefaultsAndClamp(t *testing.T) {
	l := &fakeInboxLister{}
	getInbox(inboxOps(l, false, allowAll), "", true)
	assert.Equal(t, approvalinbox.ScopeMine, l.got.Scope)
	assert.Equal(t, defaultApprovalInboxLimit, l.got.Limit)

	l = &fakeInboxLister{}
	getInbox(inboxOps(l, false, allowAll), "limit=100000", true)
	assert.Equal(t, 100, l.got.Limit)
}

func TestApprovalInbox_ScopeAllNeedsSuperAdmin(t *testing.T) {
	l := &fakeInboxLister{}
	rec := getInbox(inboxOps(l, false, allowAll), "scope=all", true)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, l.called, "a denied scope=all must never reach the store")

	l = &fakeInboxLister{}
	rec = getInbox(inboxOps(l, true, allowAll), "scope=all", true)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, approvalinbox.ScopeAll, l.got.Scope)
}

func TestApprovalInbox_PassesCallerAndCheckThrough(t *testing.T) {
	denyInvoice := func(r authz.Resource, a authz.Action) authz.Decision {
		if r == authz.ResourceInvoice {
			return authz.Decision{}
		}
		return allowAll(r, a)
	}
	l := &fakeInboxLister{}
	getInbox(inboxOps(l, false, denyInvoice), "", true)
	require.True(t, l.called)
	assert.Equal(t, 7, l.caller.EmployeeID)
	for _, s := range approvalinbox.Permitted(l.check) {
		assert.NotEqual(t, "invoice", s.Key, "a module the caller cannot read must not be permitted")
	}
}

func TestApprovalInbox_ResponseShape(t *testing.T) {
	l := &fakeInboxLister{page: approvalinbox.Page{
		Total:      30,
		Modules:    []approvalinbox.ModuleCount{{WorkflowKey: "invoice", Label: "Invoice", Count: 30}},
		Items:      []approvalinbox.Item{{WorkflowKey: "invoice", RecordUUID: "u1"}},
		HasMore:    true,
		NextCursor: "abc",
	}}
	rec := getInbox(inboxOps(l, false, allowAll), "", true)
	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["success"])
	assert.Equal(t, "mine", body["scope"])
	assert.EqualValues(t, 30, body["total"])
	assert.Equal(t, "abc", body["nextCursor"])
	assert.Equal(t, true, body["hasMore"])

	// Empty inbox: items is [] (not null), no nextCursor, hasMore false.
	l = &fakeInboxLister{page: approvalinbox.Page{Items: []approvalinbox.Item{}, Modules: []approvalinbox.ModuleCount{}}}
	rec = getInbox(inboxOps(l, false, allowAll), "", true)
	body = map[string]any{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, []any{}, body["items"])
	assert.Equal(t, false, body["hasMore"])
	assert.NotContains(t, body, "nextCursor")
}

func TestApprovalInbox_StoreErrorIs500(t *testing.T) {
	l := &fakeInboxLister{err: errors.New("boom")}
	rec := getInbox(inboxOps(l, false, allowAll), "", true)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "boom", "internal errors must not leak")
}
