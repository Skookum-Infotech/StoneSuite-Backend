package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
)

// TestNew_HTTPClientHasTimeout guards the regression this field exists for:
// R2 calls used http.DefaultClient, which has no timeout.
func TestNew_HTTPClientHasTimeout(t *testing.T) {
	tests := []struct {
		name string
		cfg  time.Duration
		want time.Duration
	}{
		{name: "configured", cfg: 7 * time.Second, want: 7 * time.Second},
		{name: "unset falls back", cfg: 0, want: fallbackHTTPTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(config.Config{CloudflareAccountID: "acct", R2AccessKeyID: "ak", R2SecretAccessKey: "sk", R2HTTPTimeout: tt.cfg})
			require.NoError(t, err)
			require.NotNil(t, c)
			assert.Equal(t, tt.want, c.http.Timeout)
			assert.Equal(t, tt.want, NewCFClient("acct", "tok", tt.cfg).http.Timeout)
		})
	}
}

// TestGet_StalledServerTimesOut proves a hung R2 response fails fast instead
// of blocking the caller (a context with no deadline, like a request ctx).
func TestGet_StalledServerTimesOut(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	hc := redirectClient(t, srv)
	hc.Timeout = 100 * time.Millisecond
	c := (&Client{accessKey: "ak", secretKey: "sk", host: "acct.r2.cloudflarestorage.com", http: hc}).WithBucket(headTestBucket)

	start := time.Now()
	_, err := c.Get(context.Background(), "some/key")
	require.Error(t, err)
	assert.Less(t, time.Since(start), 5*time.Second)
}
