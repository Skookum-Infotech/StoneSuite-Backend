package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

func TestSummarizeEmail(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st := func(state, recipient string) EmailDeliveryStatus {
		return EmailDeliveryStatus{State: state, Recipient: recipient, UpdatedAt: at, Detail: "provider text"}
	}

	tests := []struct {
		name        string
		ids         []string
		statuses    map[string]EmailDeliveryStatus
		wantStatus  string
		wantMessage bool
		wantRecips  int
		wantAtSet   bool
	}{
		{"no ids", nil, nil, EmailStateUnknown, false, 0, false},
		{"notify returned nothing", []string{"a"}, map[string]EmailDeliveryStatus{}, EmailStateUnknown, false, 0, false},
		{"single delivered", []string{"a"}, map[string]EmailDeliveryStatus{"a": st("delivered", "x@y.com")}, EmailStateDelivered, false, 1, true},
		{"single bounced carries a message", []string{"a"}, map[string]EmailDeliveryStatus{"a": st("bounced", "x@y.com")}, EmailStateBounced, true, 1, true},
		{"bounce beats delivered", []string{"a", "b"}, map[string]EmailDeliveryStatus{"a": st("delivered", "x@y.com"), "b": st("bounced", "z@y.com")}, EmailStateBounced, true, 2, true},
		{"sent is worse than delivered", []string{"a", "b"}, map[string]EmailDeliveryStatus{"a": st("delivered", "x@y.com"), "b": st("sent", "z@y.com")}, EmailStateSent, false, 2, true},
		{"delayed beats queued and sent", []string{"a", "b", "c"}, map[string]EmailDeliveryStatus{"a": st("queued", "1@y.com"), "b": st("delayed", "2@y.com"), "c": st("sent", "3@y.com")}, EmailStateDelayed, true, 3, true},
		{"complaint outranks a retry", []string{"a", "b"}, map[string]EmailDeliveryStatus{"a": st("complained", "1@y.com"), "b": st("retrying", "2@y.com")}, EmailStateComplained, true, 2, true},
		{"one unknown id among delivered stays unsure", []string{"a", "b"}, map[string]EmailDeliveryStatus{"a": st("delivered", "x@y.com")}, EmailStateUnknown, false, 1, false},
		{"state outside the vocabulary becomes unknown", []string{"a"}, map[string]EmailDeliveryStatus{"a": st("exploded", "x@y.com")}, EmailStateUnknown, false, 1, true},
		{"skipped has the turned-off message", []string{"a"}, map[string]EmailDeliveryStatus{"a": st("skipped", "x@y.com")}, EmailStateSkipped, true, 1, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SummarizeEmail(tc.ids, tc.statuses)
			assert.Equal(t, tc.wantStatus, got.Status)
			assert.Equal(t, tc.wantMessage, got.Message != "", "message = %q", got.Message)
			assert.Len(t, got.Recipients, tc.wantRecips)
			assert.Equal(t, tc.wantAtSet, got.At != nil)
			assert.NotContains(t, got.Message, "provider text", "provider detail must never be copied into the summary")
		})
	}
}

func TestSummarizeEmail_JSONShape(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	got := SummarizeEmail([]string{"a"}, map[string]EmailDeliveryStatus{
		"a": {State: "bounced", Recipient: "x@y.com", UpdatedAt: at},
	})
	raw, err := json.Marshal(got)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	assert.Equal(t, "bounced", m["emailStatus"])
	assert.Equal(t, "2026-10-01T12:00:00Z", m["emailStatusAt"])
	assert.NotEmpty(t, m["emailStatusMessage"])
	assert.Equal(t, []any{map[string]any{"email": "x@y.com", "status": "bounced"}}, m["emailRecipients"])
}

type statusRequestRecord struct {
	Secret string
	Body   struct {
		TenantID        string   `json:"tenantId"`
		NotificationIDs []string `json:"notificationIds"`
	}
}

// fakeStatusNotify records every status request and answers from a script.
type fakeStatusNotify struct {
	mu       sync.Mutex
	requests []statusRequestRecord
	respond  func(w http.ResponseWriter, ids []string)
}

func (f *fakeStatusNotify) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/deliveries/email-status" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var rec statusRequestRecord
		rec.Secret = r.Header.Get("X-Internal-Secret")
		_ = json.NewDecoder(r.Body).Decode(&rec.Body)
		f.mu.Lock()
		f.requests = append(f.requests, rec)
		f.mu.Unlock()
		f.respond(w, rec.Body.NotificationIDs)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func deliveredFor(w http.ResponseWriter, ids []string) {
	statuses := map[string]any{}
	for _, id := range ids {
		statuses[id] = map[string]any{"state": "delivered", "recipient": id + "@example.com", "detail": "", "updatedAt": "2026-10-01T12:00:00Z"}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{"statuses": statuses}})
}

func TestFetchEmailStatuses_RequestShapeAndParsing(t *testing.T) {
	fake := &fakeStatusNotify{respond: deliveredFor}
	srv := fake.server(t)
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "secret-1"}

	got, err := fetchEmailStatuses(context.Background(), cfg, "tenant-1", []string{"n1", "n2"})

	require.NoError(t, err)
	require.Len(t, fake.requests, 1)
	assert.Equal(t, "secret-1", fake.requests[0].Secret)
	assert.Equal(t, "tenant-1", fake.requests[0].Body.TenantID)
	assert.Equal(t, []string{"n1", "n2"}, fake.requests[0].Body.NotificationIDs)
	assert.Equal(t, "delivered", got["n1"].State)
	assert.Equal(t, "n2@example.com", got["n2"].Recipient)
}

func TestFetchEmailStatuses_DedupesAndChunks(t *testing.T) {
	fake := &fakeStatusNotify{respond: deliveredFor}
	srv := fake.server(t)
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "k"}

	ids := make([]string, 0, 451)
	for i := 0; i < 450; i++ {
		ids = append(ids, "n"+strconv.Itoa(1000+i))
	}
	ids = append(ids, "n"+strconv.Itoa(1000)) // an exact duplicate of the first id must be sent once

	got, err := fetchEmailStatuses(context.Background(), cfg, "t", ids)

	require.NoError(t, err)
	require.Len(t, fake.requests, 3, "450 distinct ids (the duplicate is dropped) at 200 per request is 3 requests")
	assert.Len(t, fake.requests[0].Body.NotificationIDs, 200)
	assert.Len(t, fake.requests[1].Body.NotificationIDs, 200)
	assert.Len(t, fake.requests[2].Body.NotificationIDs, 50)
	assert.Len(t, got, 450)
}

func TestFetchEmailStatuses_NothingToAsk(t *testing.T) {
	fake := &fakeStatusNotify{respond: deliveredFor}
	srv := fake.server(t)
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "k"}

	got, err := fetchEmailStatuses(context.Background(), cfg, "t", []string{"", ""})

	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, fake.requests, "no ids means no request")
}

func TestFetchEmailStatuses_NotConfiguredIsEmptyNotError(t *testing.T) {
	got, err := fetchEmailStatuses(context.Background(), config.Config{}, "t", []string{"n1"})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFetchEmailStatuses_Failures(t *testing.T) {
	tests := []struct {
		name    string
		respond func(w http.ResponseWriter, ids []string)
	}{
		{"notify 500", func(w http.ResponseWriter, _ []string) { w.WriteHeader(http.StatusInternalServerError) }},
		{"notify 400", func(w http.ResponseWriter, _ []string) { w.WriteHeader(http.StatusBadRequest) }},
		{"not json", func(w http.ResponseWriter, _ []string) { _, _ = w.Write([]byte("<html>")) }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeStatusNotify{respond: tc.respond}
			srv := fake.server(t)
			cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "k"}

			got, err := fetchEmailStatuses(context.Background(), cfg, "t", []string{"n1"})

			require.Error(t, err)
			assert.NotNil(t, got, "a failure still returns a usable (empty) map")
			assert.Empty(t, got)
		})
	}
}

func TestEmailStatuses_IsFailOpen(t *testing.T) {
	// Unreachable notify: the public entry point swallows the error.
	orig := config.AppConfig
	config.AppConfig.NotifyURL = "http://127.0.0.1:1" // nothing listens here
	config.AppConfig.NotifyAPIKey = "k"
	t.Cleanup(func() { config.AppConfig = orig })

	got := EmailStatuses(context.Background(), "t", []string{"n1"})

	assert.NotNil(t, got)
	assert.Empty(t, got)
}
