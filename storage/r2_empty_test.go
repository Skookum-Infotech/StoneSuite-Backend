package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testEmptyBucketName = "ss-acme"

// fakeBucket is an in-memory S3 endpoint serving paginated ListObjectsV2 and
// object DELETE for one bucket.
type fakeBucket struct {
	mu          sync.Mutex
	pages       [][]string // keys returned on each list page
	deleted     []string
	listQueries []url.Values
	listStatus  int    // when non-zero, list requests fail with it
	failDelete  string // when set, deleting this key fails with 500
	badAuth     int    // count of requests missing a SigV4 Authorization header
}

func (f *fakeBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		f.badAuth++
	}

	switch r.Method {
	case http.MethodGet:
		f.listQueries = append(f.listQueries, r.URL.Query())
		if r.URL.Path != "/"+testEmptyBucketName {
			http.Error(w, "unexpected list path "+r.URL.Path, http.StatusBadRequest)
			return
		}
		if f.listStatus != 0 {
			http.Error(w, "boom", f.listStatus)
			return
		}
		page := 0
		if tok := r.URL.Query().Get("continuation-token"); tok != "" {
			_, _ = fmt.Sscanf(tok, "page-%d", &page)
		}
		var keys []string
		if page < len(f.pages) {
			keys = f.pages[page]
		}
		truncated := page+1 < len(f.pages)
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
		fmt.Fprintf(&b, "<IsTruncated>%t</IsTruncated>", truncated)
		if truncated {
			fmt.Fprintf(&b, "<NextContinuationToken>page-%d</NextContinuationToken>", page+1)
		}
		for _, k := range keys {
			fmt.Fprintf(&b, "<Contents><Key>%s</Key></Contents>", k)
		}
		b.WriteString("</ListBucketResult>")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(b.String()))

	case http.MethodDelete:
		key := strings.TrimPrefix(r.URL.Path, "/"+testEmptyBucketName+"/")
		f.deleted = append(f.deleted, key)
		if key == f.failDelete {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
	}
}

func newEmptyBucketClient(t *testing.T, f *fakeBucket) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	redirectDefaultClient(t, srv)
	c := &Client{accessKey: "AKIATEST", secretKey: "secret", host: "acct.r2.cloudflarestorage.com"}
	return c.WithBucket(testEmptyBucketName)
}

func TestEmptyBucket_NilClient(t *testing.T) {
	var c *Client
	_, err := c.EmptyBucket(context.Background())
	assert.ErrorIs(t, err, ErrStorageNotConfigured)
}

func TestEmptyBucket_RefusesWhenNoBucketSelected(t *testing.T) {
	c := &Client{accessKey: "AKIATEST", secretKey: "secret", host: "acct.r2.cloudflarestorage.com"}
	_, err := c.EmptyBucket(context.Background())
	require.Error(t, err, "without a bucket the list call would hit the account root")
	assert.Contains(t, err.Error(), "bucket")
}

func TestEmptyBucket_DeletesEveryKeyAcrossPages(t *testing.T) {
	f := &fakeBucket{pages: [][]string{
		{"a.pdf", "dir/b c.png"},
		{"z.txt"},
	}}
	c := newEmptyBucketClient(t, f)

	n, err := c.EmptyBucket(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 3, n)
	got := append([]string(nil), f.deleted...)
	sort.Strings(got)
	assert.Equal(t, []string{"a.pdf", "dir/b c.png", "z.txt"}, got)

	require.Len(t, f.listQueries, 2, "one list call per page")
	assert.Equal(t, "2", f.listQueries[0].Get("list-type"))
	assert.Empty(t, f.listQueries[0].Get("continuation-token"))
	assert.Equal(t, "page-1", f.listQueries[1].Get("continuation-token"))
	assert.Zero(t, f.badAuth, "every request must carry a SigV4 Authorization header")
}

func TestEmptyBucket_AlreadyEmpty(t *testing.T) {
	f := &fakeBucket{}
	c := newEmptyBucketClient(t, f)

	n, err := c.EmptyBucket(context.Background())

	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Empty(t, f.deleted)
}

func TestEmptyBucket_ListFailureStops(t *testing.T) {
	f := &fakeBucket{pages: [][]string{{"a"}}, listStatus: http.StatusInternalServerError}
	c := newEmptyBucketClient(t, f)

	_, err := c.EmptyBucket(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Empty(t, f.deleted)
}

func TestEmptyBucket_DeleteFailureStopsBeforeNextPage(t *testing.T) {
	f := &fakeBucket{pages: [][]string{{"a", "b"}, {"c"}}, failDelete: "a"}
	c := newEmptyBucketClient(t, f)

	n, err := c.EmptyBucket(context.Background())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "a", "the failing key is named in the error")
	assert.Zero(t, n)
	assert.Equal(t, []string{"a"}, f.deleted, "stops at the first failure")
	assert.Len(t, f.listQueries, 1, "the second page is never requested")
}
