package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		body   map[string]any
		want   time.Duration
	}{
		{"header", "5", nil, 5 * time.Second},
		{"body", "", map[string]any{"retryAfter": float64(7)}, 7 * time.Second},
		{"header wins", "2", map[string]any{"retryAfter": float64(7)}, 2 * time.Second},
		{"none", "", nil, 0},
		{"garbage", "soon", nil, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parseRetryAfter(tt.header, tt.body))
		})
	}
}

func TestAskRetry(t *testing.T) {
	tests := []struct {
		name      string
		retryHdr  string
		limited   int32 // number of leading 429 responses
		wantCalls int32
		wantWait  time.Duration
		wantOK    bool
	}{
		{"no 429", "", 0, 1, 0, true},
		{"one 429 then ok", "3", 1, 2, 3 * time.Second, true},
		{"cap at 30s", "120", 1, 2, maxRetryWait, true},
		{"still 429", "1", 5, 2, time.Second, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := atomic.AddInt32(&calls, 1)
				if n <= tt.limited {
					if tt.retryHdr != "" {
						w.Header().Set("Retry-After", tt.retryHdr)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusTooManyRequests)
					_, _ = w.Write([]byte(`{"success":false}`))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("event: done\ndata: {\"answer\":\"hi\",\"conversation_id\":\"c1\"}\n\n"))
			}))
			defer srv.Close()

			c := newAPIClient(srv.URL, "tok")
			var waited time.Duration
			c.sleep = func(_ context.Context, d time.Duration) { waited = d }
			res := c.askRetry(context.Background(), "q", "")
			assert.Equal(t, tt.wantCalls, atomic.LoadInt32(&calls))
			assert.Equal(t, tt.wantWait, waited)
			if tt.wantOK {
				assert.Equal(t, http.StatusOK, res.HTTPStatus)
			} else {
				assert.Equal(t, http.StatusTooManyRequests, res.HTTPStatus)
			}
		})
	}
}

func TestCreateConversation(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		want    string
		wantErr bool
	}{
		{"ok", http.StatusCreated, `{"success":true,"data":{"id":"abc"}}`, "abc", false},
		{"server error", http.StatusInternalServerError, `{"success":false}`, "", true},
		{"empty id", http.StatusCreated, `{"success":true,"data":{}}`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/tenant/ai/conversations", r.URL.Path)
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()
			got, err := newAPIClient(srv.URL, "tok").createConversation(context.Background())
			assert.Equal(t, tt.wantErr, err != nil)
			assert.Equal(t, tt.want, got)
		})
	}
}
