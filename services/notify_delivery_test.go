package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

// resendDomainError is the real provider text from a refused first attempt; it
// must reach the log detail but never a client-facing message.
const resendDomainError = `resend: unexpected status 403: {"message":"The stonesuite.local domain is not verified."}`

const (
	testPollInterval = 5 * time.Millisecond
	testWaitTimeout  = 250 * time.Millisecond
)

func TestClassifyEmail(t *testing.T) {
	tests := []struct {
		name       string
		rows       []deliveryRecord
		wantSettle bool
		wantStatus EmailStatus
		wantDetail string
	}{
		{"sent", []deliveryRecord{{Channel: "email", Status: "sent"}}, true, EmailStatusSent, ""},
		{"first attempt refused is retrying", []deliveryRecord{{Channel: "email", Status: "retrying", LastError: resendDomainError}}, true, EmailStatusFailed, resendDomainError},
		{"terminal failure", []deliveryRecord{{Channel: "email", Status: "failed", LastError: "boom"}}, true, EmailStatusFailed, "boom"},
		{"pending is unsettled", []deliveryRecord{{Channel: "email", Status: "pending"}}, false, "", ""},
		{"processing is unsettled", []deliveryRecord{{Channel: "email", Status: "processing"}}, false, "", ""},
		{"skipped by preference", []deliveryRecord{{Channel: "email", Status: "skipped"}}, true, EmailStatusSkipped, ""},
		{"other channels are ignored", []deliveryRecord{
			{Channel: "in_app", Status: "sent"}, {Channel: "push", Status: "failed", LastError: "x"},
			{Channel: "email", Status: "sent"},
		}, true, EmailStatusSent, ""},
		// No email row is "cannot verify", not "turned off": notify records
		// "skipped" explicitly, and the row never appears later, so it is
		// settled (do not wait) but reported as unverified.
		{"no email row is unverifiable, not skipped", []deliveryRecord{{Channel: "in_app", Status: "sent"}}, true, EmailStatusPending, deliveryDetailNoEmailRow},
		{"no rows at all is unverifiable, not skipped", nil, true, EmailStatusPending, deliveryDetailNoEmailRow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyEmail(tt.rows)
			assert.Equal(t, tt.wantSettle, got.settled)
			assert.Equal(t, tt.wantStatus, got.status)
			assert.Equal(t, tt.wantDetail, got.detail)
		})
	}
}

func TestEmailOutcome_UserMessage(t *testing.T) {
	tests := []struct {
		name    string
		outcome EmailOutcome
		want    string
	}{
		{"none attempted", EmailOutcome{}, ""},
		{"sent", EmailOutcome{Status: EmailStatusSent}, ""},
		{"failed", EmailOutcome{Status: EmailStatusFailed, Detail: resendDomainError}, msgEmailFailed},
		{"pending", EmailOutcome{Status: EmailStatusPending}, msgEmailPending},
		{"skipped", EmailOutcome{Status: EmailStatusSkipped}, msgEmailSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.outcome.UserMessage())
			if tt.outcome.Detail != "" {
				assert.NotContains(t, tt.outcome.UserMessage(), tt.outcome.Detail, "provider detail must not reach clients")
				assert.NotContains(t, tt.outcome.UserMessage(), "403")
			}
		})
	}
}

func TestConfirmEmailDelivery_SendErrorIsFailedWithoutPolling(t *testing.T) {
	got := ConfirmEmailDelivery(context.Background(), "tenant-1", NotificationResult{}, errors.New("notify service not configured"))
	assert.Equal(t, EmailStatusFailed, got.Status)
	assert.True(t, got.Attempted())
	assert.False(t, got.Sent())
	assert.Equal(t, "notify service not configured", got.Detail)
}

// deliveryScript serves notify's delivery log: for each notification id, the
// successive bodies returned by consecutive polls (the last one repeats).
type deliveryScript struct {
	mu       sync.Mutex
	bodies   map[string][]string
	calls    map[string]int
	lastReq  *http.Request
	httpCode int
}

func newDeliveryServer(t *testing.T, bodies map[string][]string) (*httptest.Server, *deliveryScript) {
	t.Helper()
	s := &deliveryScript{bodies: bodies, calls: map[string]int{}, httpCode: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.lastReq = r.Clone(context.Background())
		id := strings.Split(strings.Trim(r.URL.Path, "/"), "/")[2] // api/notifications/{id}/deliveries
		seq := s.bodies[id]
		i := s.calls[id]
		if i >= len(seq) {
			i = len(seq) - 1
		}
		s.calls[id]++
		w.WriteHeader(s.httpCode)
		if len(seq) > 0 {
			_, _ = w.Write([]byte(seq[i]))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, s
}

func deliveriesBody(rows ...deliveryRecord) string {
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf(`{"channel":%q,"status":%q,"lastError":%q}`, r.Channel, r.Status, r.LastError))
	}
	return `{"success":true,"data":{"deliveries":[` + strings.Join(parts, ",") + `]}}`
}

func email(status, lastErr string) string {
	return deliveriesBody(deliveryRecord{Channel: "in_app", Status: "skipped"}, deliveryRecord{Channel: "email", Status: status, LastError: lastErr})
}

func TestAwaitEmailDelivery(t *testing.T) {
	tests := []struct {
		name       string
		ids        []string
		bodies     map[string][]string
		wantStatus EmailStatus
		wantDetail string
	}{
		{
			name:       "sent on the first read",
			ids:        []string{"n1"},
			bodies:     map[string][]string{"n1": {email("sent", "")}},
			wantStatus: EmailStatusSent,
		},
		{
			name:       "waits through pending and processing, then sent",
			ids:        []string{"n1"},
			bodies:     map[string][]string{"n1": {email("pending", ""), email("processing", ""), email("sent", "")}},
			wantStatus: EmailStatusSent,
		},
		{
			name:       "provider refuses the first attempt (the unverified-domain case)",
			ids:        []string{"n1"},
			bodies:     map[string][]string{"n1": {email("pending", ""), email("retrying", resendDomainError)}},
			wantStatus: EmailStatusFailed,
			wantDetail: resendDomainError,
		},
		{
			name:       "never leaves pending within the window",
			ids:        []string{"n1"},
			bodies:     map[string][]string{"n1": {email("pending", "")}},
			wantStatus: EmailStatusPending,
		},
		{
			name:       "skipped when the recipient turned email off",
			ids:        []string{"n1"},
			bodies:     map[string][]string{"n1": {email("skipped", "")}},
			wantStatus: EmailStatusSkipped,
		},
		{
			name:       "multiple recipients: any failure wins over sent",
			ids:        []string{"n1", "n2"},
			bodies:     map[string][]string{"n1": {email("sent", "")}, "n2": {email("failed", "bad address")}},
			wantStatus: EmailStatusFailed,
			wantDetail: "bad address",
		},
		{
			name:       "multiple recipients: failure wins over one still pending",
			ids:        []string{"n1", "n2"},
			bodies:     map[string][]string{"n1": {email("pending", "")}, "n2": {email("retrying", "bad address")}},
			wantStatus: EmailStatusFailed,
			wantDetail: "bad address",
		},
		{
			name:       "multiple recipients: all sent",
			ids:        []string{"n1", "n2"},
			bodies:     map[string][]string{"n1": {email("sent", "")}, "n2": {email("pending", ""), email("sent", "")}},
			wantStatus: EmailStatusSent,
		},
		{
			name:       "multiple recipients: one sent, one still pending is pending",
			ids:        []string{"n1", "n2"},
			bodies:     map[string][]string{"n1": {email("sent", "")}, "n2": {email("pending", "")}},
			wantStatus: EmailStatusPending,
		},
		{
			name:       "notify has no email row for the notification: unverified, reported without waiting",
			ids:        []string{"n1"},
			bodies:     map[string][]string{"n1": {deliveriesBody(deliveryRecord{Channel: "in_app", Status: "sent"})}},
			wantStatus: EmailStatusPending,
			wantDetail: deliveryDetailNoEmailRow,
		},
		{
			name:       "no notification ids to check",
			ids:        nil,
			bodies:     nil,
			wantStatus: EmailStatusPending,
			wantDetail: deliveryDetailNoIDs,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newDeliveryServer(t, tt.bodies)
			cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "nk_test"}

			got := awaitEmailDelivery(context.Background(), cfg, "tenant-1", tt.ids, testPollInterval, testWaitTimeout)

			assert.Equal(t, tt.wantStatus, got.Status)
			assert.Equal(t, tt.wantDetail, got.Detail)
		})
	}
}

func TestAwaitEmailDelivery_CallsNotifyCorrectly(t *testing.T) {
	srv, script := newDeliveryServer(t, map[string][]string{"n 1": {email("sent", "")}})
	cfg := config.Config{NotifyURL: srv.URL + "/", NotifyAPIKey: "nk_test_secret"}

	got := awaitEmailDelivery(context.Background(), cfg, "tenant-1", []string{"n 1"}, testPollInterval, testWaitTimeout)
	require.Equal(t, EmailStatusSent, got.Status)

	script.mu.Lock()
	defer script.mu.Unlock()
	require.NotNil(t, script.lastReq)
	assert.Equal(t, "/api/notifications/n%201/deliveries", script.lastReq.URL.EscapedPath())
	assert.Equal(t, "tenant-1", script.lastReq.URL.Query().Get("tenantId"), "notify scopes the log by tenant")
	assert.Equal(t, "nk_test_secret", script.lastReq.Header.Get("X-Internal-Secret"))
}

func TestAwaitEmailDelivery_UnreadableLogIsPendingNotSent(t *testing.T) {
	// Not being able to read the log must never be reported as success.
	srv, script := newDeliveryServer(t, map[string][]string{"n1": {`{"success":false}`}})
	script.httpCode = http.StatusInternalServerError
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "nk_test"}

	got := awaitEmailDelivery(context.Background(), cfg, "tenant-1", []string{"n1"}, testPollInterval, testWaitTimeout)

	assert.Equal(t, EmailStatusPending, got.Status)
	assert.Contains(t, got.Detail, "500")
}

func TestAwaitEmailDelivery_NotifyNotConfigured(t *testing.T) {
	got := awaitEmailDelivery(context.Background(), config.Config{}, "tenant-1", []string{"n1"}, testPollInterval, testWaitTimeout)

	assert.Equal(t, EmailStatusPending, got.Status)
	assert.Contains(t, got.Detail, "not configured")
}

func TestAwaitEmailDelivery_StopsWhenRequestIsCancelled(t *testing.T) {
	srv, _ := newDeliveryServer(t, map[string][]string{"n1": {email("pending", "")}})
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "nk_test"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	got := awaitEmailDelivery(ctx, cfg, "tenant-1", []string{"n1"}, testPollInterval, 5*time.Second)

	assert.Equal(t, EmailStatusPending, got.Status)
	assert.Less(t, time.Since(start), time.Second, "a cancelled request must not wait out the full window")
}

func TestAwaitEmailDelivery_NoEmailRowDoesNotWaitOutTheWindow(t *testing.T) {
	// The row is written in the same call that answers "created", so if it is
	// missing it will not appear later — the result must come back at once.
	srv, _ := newDeliveryServer(t, map[string][]string{"n1": {deliveriesBody(deliveryRecord{Channel: "in_app", Status: "sent"})}})
	cfg := config.Config{NotifyURL: srv.URL, NotifyAPIKey: "nk_test"}

	start := time.Now()
	got := awaitEmailDelivery(context.Background(), cfg, "tenant-1", []string{"n1"}, testPollInterval, 5*time.Second)

	assert.Equal(t, EmailStatusPending, got.Status)
	assert.False(t, got.Sent())
	assert.Less(t, time.Since(start), time.Second)
}
