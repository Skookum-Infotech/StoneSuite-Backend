package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const headTestBucket = "ss-acme"

func newHeadTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	redirectDefaultClient(t, srv)
	c := &Client{accessKey: "ak", secretKey: "sk", host: "acct.r2.cloudflarestorage.com"}
	return c.WithBucket(headTestBucket)
}

func TestHeadAndGetLimited_NilClient(t *testing.T) {
	var c *Client
	_, err := c.Head(context.Background(), "k")
	assert.ErrorIs(t, err, ErrStorageNotConfigured)
	_, err = c.GetLimited(context.Background(), "k", 10)
	assert.ErrorIs(t, err, ErrStorageNotConfigured)
}

func TestHead(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		wantErr  error
		wantAny  bool
		wantInfo ObjectInfo
	}{
		{name: "ok", status: http.StatusOK, wantInfo: ObjectInfo{Size: 5, ContentType: "application/pdf"}},
		{name: "not found", status: http.StatusNotFound, wantErr: ErrNotFound},
		{name: "server error", status: http.StatusInternalServerError, wantAny: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newHeadTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodHead, r.Method)
				assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 "))
				assert.Equal(t, "/"+headTestBucket+"/a/b.pdf", r.URL.Path)
				if tt.status == http.StatusOK {
					w.Header().Set("Content-Type", "application/pdf")
					w.Header().Set("Content-Length", "5")
				}
				w.WriteHeader(tt.status)
			})
			info, err := c.Head(context.Background(), "a/b.pdf")
			switch {
			case tt.wantErr != nil:
				assert.ErrorIs(t, err, tt.wantErr)
			case tt.wantAny:
				assert.Error(t, err)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.wantInfo, info)
			}
		})
	}
}

func TestGetLimited(t *testing.T) {
	const maxBytes = 5
	tests := []struct {
		name    string
		status  int
		body    string
		wantErr error
		wantAny bool
	}{
		{name: "within limit", status: http.StatusOK, body: "12345"},
		{name: "over limit", status: http.StatusOK, body: "123456", wantErr: ErrTooLarge},
		{name: "not found", status: http.StatusNotFound, wantErr: ErrNotFound},
		{name: "server error", status: http.StatusInternalServerError, wantAny: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newHeadTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			})
			got, err := c.GetLimited(context.Background(), "k", maxBytes)
			switch {
			case tt.wantErr != nil:
				assert.ErrorIs(t, err, tt.wantErr)
			case tt.wantAny:
				assert.Error(t, err)
			default:
				require.NoError(t, err)
				assert.Equal(t, tt.body, string(got))
			}
		})
	}
}
