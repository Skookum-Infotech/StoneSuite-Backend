package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestDeadline(t *testing.T) {
	const d = 50 * time.Millisecond
	prefixes := []string{"/api/internal/automation/"}
	suffixes := []string{"/ask/stream"}

	tests := []struct {
		name         string
		timeout      time.Duration
		path         string
		wantDeadline bool
	}{
		{name: "tenant route gets a deadline", timeout: d, path: "/api/tenant/invoices", wantDeadline: true},
		{name: "stream route exempt", timeout: d, path: "/api/tenant/ai/ask/stream", wantDeadline: false},
		{name: "automation tick exempt", timeout: d, path: "/api/internal/automation/tick", wantDeadline: false},
		{name: "other internal route not exempt", timeout: d, path: "/api/internal/email/inbound", wantDeadline: true},
		{name: "non-stream ask not exempt", timeout: d, path: "/api/tenant/ai/ask", wantDeadline: true},
		{name: "disabled when non-positive", timeout: 0, path: "/api/tenant/invoices", wantDeadline: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				gotDeadline bool
				remaining   time.Duration
			)
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var dl time.Time
				dl, gotDeadline = r.Context().Deadline()
				remaining = time.Until(dl)
			})
			rec := httptest.NewRecorder()
			Deadline(tt.timeout, prefixes, suffixes, next).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			assert.Equal(t, tt.wantDeadline, gotDeadline)
			if tt.wantDeadline {
				assert.LessOrEqual(t, remaining, tt.timeout)
			}
		})
	}
}

// TestDeadline_CancelsSlowHandler proves the point of the middleware: work
// that outlives the deadline sees its context cancelled.
func TestDeadline_CancelsSlowHandler(t *testing.T) {
	cancelled := make(chan struct{})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			close(cancelled)
		case <-time.After(5 * time.Second):
		}
	})
	Deadline(20*time.Millisecond, nil, nil, next).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/tenant/x", nil))

	select {
	case <-cancelled:
	default:
		t.Fatal("handler context was not cancelled by the deadline")
	}
}
