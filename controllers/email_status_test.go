package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

func idxWith(entries map[string]string) emailStatusIndex {
	idx := emailStatusIndex{}
	for id, state := range entries {
		idx[id] = services.EmailDeliveryStatus{State: state, Recipient: id + "@example.com", Detail: "provider text", UpdatedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	}
	return idx
}

func TestEmailStatusIndex_Summary(t *testing.T) {
	idx := idxWith(map[string]string{"a": "delivered", "b": "bounced"})

	assert.Equal(t, services.EmailStateBounced, idx.summary([]string{"a", "b"}).Status)
	assert.Equal(t, services.EmailStateUnknown, idx.summary(nil).Status)
	assert.Equal(t, services.EmailStateUnknown, emailStatusIndex(nil).summary([]string{"a"}).Status, "a nil index (lookup skipped) reads as unknown, not a panic")
}

func TestWithEmailSummary_AddsCamelCaseKeysOnly(t *testing.T) {
	idx := idxWith(map[string]string{"a": "bounced"})
	view := withEmailSummary(map[string]any{"id": "row-1"}, idx.summary([]string{"a"}))

	raw, err := json.Marshal(view)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "row-1", got["id"], "existing keys are untouched")
	assert.Equal(t, "bounced", got["emailStatus"])
	assert.NotEmpty(t, got["emailStatusMessage"])
	assert.NotEmpty(t, got["emailStatusAt"])
	assert.Len(t, got["emailRecipients"], 1)
	assert.NotContains(t, string(raw), "provider text", "provider detail must never be serialised")
}

func TestWithEmailSummary_UnknownOmitsOptionalKeys(t *testing.T) {
	view := withEmailSummary(map[string]any{}, services.SummarizeEmail(nil, nil))

	assert.Equal(t, services.EmailStateUnknown, view["emailStatus"])
	assert.NotContains(t, view, "emailStatusMessage")
	assert.NotContains(t, view, "emailStatusAt")
	assert.NotContains(t, view, "emailRecipients")
}

func TestDocumentSendViews(t *testing.T) {
	sends := []workflow.DocumentSend{
		{ID: "s1", SentTo: "x@y.com", NotifyNotificationIDs: []string{"a", "b"}},
		{ID: "s2", SentTo: "z@y.com"}, // predates notify ids
	}
	views := documentSendViews(sends, idxWith(map[string]string{"a": "delivered", "b": "bounced"}))

	require.Len(t, views, 2)
	assert.Equal(t, services.EmailStateBounced, views[0].Status)
	assert.Equal(t, services.EmailStateUnknown, views[1].Status)

	raw, err := json.Marshal(views[0])
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "s1", got["id"], "DocumentSend's own keys survive embedding")
	assert.Equal(t, "bounced", got["emailStatus"])
	assert.NotContains(t, string(raw), "provider text")

	assert.NotNil(t, documentSendViews(nil, nil), "an empty history serialises as [] not null")
	assert.Empty(t, documentSendViews(nil, nil))
}

func TestUserInviteViews_KeepLegacyPascalCaseAndAddCamelCase(t *testing.T) {
	invites := []tenancy.UserInvite{{ID: "i1", Email: "a@b.com", NotifyNotificationIDs: []string{"a"}}}
	views := userInviteViews(invites, idxWith(map[string]string{"a": "delayed"}))

	raw, err := json.Marshal(views[0])
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "i1", got["ID"], "the existing PascalCase keys the UI reads are unchanged")
	assert.Equal(t, "a@b.com", got["Email"])
	assert.Equal(t, "delayed", got["emailStatus"])
}

func TestTenantInviteViews(t *testing.T) {
	invites := []tenancy.Invite{{ID: "t1", ContactEmail: "owner@acme.com", Token: "tok", NotifyNotificationIDs: []string{"a"}}}
	views := tenantInviteViews(invites, idxWith(map[string]string{"a": "suppressed"}))

	require.Len(t, views, 1)
	assert.Equal(t, "t1", views[0]["id"])
	assert.Equal(t, "owner@acme.com", views[0]["contactEmail"])
	assert.Equal(t, "suppressed", views[0]["emailStatus"])
}

func TestPersistNotifyIDs(t *testing.T) {
	var gotRow string
	var gotIDs []string
	set := func(_ context.Context, id string, ids []string) error { gotRow, gotIDs = id, ids; return nil }

	t.Run("stores ids after a clean send", func(t *testing.T) {
		gotRow, gotIDs = "", nil
		persistNotifyIDs(context.Background(), "test invite", "row-1", services.NotificationResult{NotificationIDs: []string{"n1"}}, nil, set)
		assert.Equal(t, "row-1", gotRow)
		assert.Equal(t, []string{"n1"}, gotIDs)
	})

	t.Run("does nothing when the send failed", func(t *testing.T) {
		gotRow, gotIDs = "", nil
		persistNotifyIDs(context.Background(), "test invite", "row-1", services.NotificationResult{NotificationIDs: []string{"n1"}}, errors.New("notify down"), set)
		assert.Empty(t, gotRow)
	})

	t.Run("does nothing when notify returned no ids", func(t *testing.T) {
		gotRow, gotIDs = "", nil
		persistNotifyIDs(context.Background(), "test invite", "row-1", services.NotificationResult{}, nil, set)
		assert.Empty(t, gotRow)
	})

	t.Run("a persistence failure is swallowed — the send already happened", func(t *testing.T) {
		failing := func(context.Context, string, []string) error { return errors.New("db down") }
		assert.NotPanics(t, func() {
			persistNotifyIDs(context.Background(), "test invite", "row-1", services.NotificationResult{NotificationIDs: []string{"n1"}}, nil, failing)
		})
	})
}
