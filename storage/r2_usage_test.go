package storage

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sizedObject is one object a fake list page returns.
type sizedObject struct {
	key  string
	size int64
}

// sizedBucket serves paginated ListObjectsV2 responses that carry <Size>.
type sizedBucket struct {
	pages      [][]sizedObject
	listStatus int
	paths      []string
}

func (f *sizedBucket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.paths = append(f.paths, r.URL.Path)
	if f.listStatus != 0 {
		http.Error(w, "boom", f.listStatus)
		return
	}
	page := 0
	if tok := r.URL.Query().Get("continuation-token"); tok != "" {
		_, _ = fmt.Sscanf(tok, "page-%d", &page)
	}
	truncated := page+1 < len(f.pages)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult>`)
	fmt.Fprintf(&b, "<IsTruncated>%t</IsTruncated>", truncated)
	if truncated {
		fmt.Fprintf(&b, "<NextContinuationToken>page-%d</NextContinuationToken>", page+1)
	}
	if page < len(f.pages) {
		for _, o := range f.pages[page] {
			fmt.Fprintf(&b, "<Contents><Key>%s</Key><Size>%d</Size></Contents>", o.key, o.size)
		}
	}
	b.WriteString("</ListBucketResult>")
	_, _ = w.Write([]byte(b.String()))
}

func newSizedBucketClient(t *testing.T, f *sizedBucket) *Client {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := &Client{accessKey: "AKIATEST", secretKey: "secret", host: "acct.r2.cloudflarestorage.com", http: redirectClient(t, srv)}
	return c.WithBucket(testEmptyBucketName)
}

func TestUsage(t *testing.T) {
	tests := []struct {
		name  string
		pages [][]sizedObject
		want  BucketUsage
	}{
		{name: "empty bucket", pages: nil, want: BucketUsage{}},
		{name: "single page", pages: [][]sizedObject{{{"a", 10}, {"b", 32}}}, want: BucketUsage{Bytes: 42, Objects: 2}},
		{
			name:  "sums across pages",
			pages: [][]sizedObject{{{"a", 1024}}, {{"b", 2048}, {"c", 1}}, {{"d", 0}}},
			want:  BucketUsage{Bytes: 3073, Objects: 4},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &sizedBucket{pages: tc.pages}
			got, err := newSizedBucketClient(t, f).Usage(context.Background())
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
			for _, p := range f.paths {
				assert.Equal(t, "/"+testEmptyBucketName, p, "must list only the selected bucket")
			}
		})
	}
}

func TestUsage_ListFailure(t *testing.T) {
	_, err := newSizedBucketClient(t, &sizedBucket{listStatus: http.StatusForbidden}).Usage(context.Background())
	assert.ErrorContains(t, err, "HTTP 403")
}

func TestUsage_Guards(t *testing.T) {
	var nilClient *Client
	_, err := nilClient.Usage(context.Background())
	assert.ErrorIs(t, err, ErrStorageNotConfigured)

	_, err = (&Client{}).Usage(context.Background())
	assert.ErrorContains(t, err, "no bucket selected")
}
