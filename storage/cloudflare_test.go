package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCFClient(t *testing.T, h http.HandlerFunc) *CFClient {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &CFClient{
		accountID: "acct",
		apiToken:  "tok",
		http:      &http.Client{Transport: redirectTransport(t, srv)},
	}
}

func TestDeleteBucket_SendsAuthenticatedDeleteToBucketPath(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	c := newTestCFClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})

	err := c.DeleteBucket(context.Background(), "ss-acme")

	require.NoError(t, err)
	assert.Equal(t, http.MethodDelete, gotMethod)
	assert.Equal(t, "/client/v4/accounts/acct/r2/buckets/ss-acme", gotPath)
	assert.Equal(t, "Bearer tok", gotAuth)
}

func TestDeleteBucket_StatusHandling(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
		wantIn  string
	}{
		{"deleted", http.StatusOK, false, ""},
		{"already gone is success (idempotent)", http.StatusNotFound, false, ""},
		{"not empty is surfaced", http.StatusConflict, true, "409"},
		{"server error is surfaced", http.StatusInternalServerError, true, "500"},
		{"forbidden is surfaced", http.StatusForbidden, true, "403"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestCFClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(`{"success":false}`))
			})

			err := c.DeleteBucket(context.Background(), "ss-acme")

			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantIn)
		})
	}
}

func TestDeleteBucket_EscapesBucketName(t *testing.T) {
	var gotRawPath string
	c := newTestCFClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotRawPath = r.URL.EscapedPath()
		w.WriteHeader(http.StatusOK)
	})

	require.NoError(t, c.DeleteBucket(context.Background(), "a/b"))

	assert.Equal(t, "/client/v4/accounts/acct/r2/buckets/"+url.PathEscape("a/b"), gotRawPath)
}

func TestDeleteBucket_RefusesEmptyName(t *testing.T) {
	called := false
	c := newTestCFClient(t, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	err := c.DeleteBucket(context.Background(), "")

	require.Error(t, err)
	assert.False(t, called, "an empty name would address the bucket collection")
}

func TestDeleteBucket_NilClient(t *testing.T) {
	var c *CFClient
	err := c.DeleteBucket(context.Background(), "ss-acme")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
}
