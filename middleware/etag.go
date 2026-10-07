package middleware

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

const (
	headerETag         = "ETag"
	headerIfNoneMatch  = "If-None-Match"
	headerCacheControl = "Cache-Control"
	// cacheControlRevalidate lets the browser keep a copy but forces it to
	// revalidate every time, so a 304 is cheap and data is never stale.
	cacheControlRevalidate = "private, no-cache"
	etagHashBytes          = 16
)

// bufferedWriter captures a handler's response so ETag can hash the body
// before anything reaches the client.
type bufferedWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferedWriter) Header() http.Header { return b.header }

func (b *bufferedWriter) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferedWriter) Write(p []byte) (int, error) {
	if b.status == 0 {
		b.status = http.StatusOK
	}
	return b.body.Write(p)
}

// ETag wraps a GET handler: successful (200) responses get a content-hash
// ETag and "Cache-Control: private, no-cache", and a matching If-None-Match
// short-circuits to 304 with no body. Non-GET requests and non-200 responses
// pass through untouched. The handler still runs on every request, so this
// saves bandwidth and parse time, never freshness or authorization.
func ETag(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		bw := &bufferedWriter{header: make(http.Header)}
		next.ServeHTTP(bw, r)

		for k, v := range bw.header {
			w.Header()[k] = v
		}
		status := bw.status
		if status == 0 {
			status = http.StatusOK
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write(bw.body.Bytes())
			return
		}

		sum := sha256.Sum256(bw.body.Bytes())
		tag := `"` + hex.EncodeToString(sum[:etagHashBytes]) + `"`
		w.Header().Set(headerETag, tag)
		w.Header().Set(headerCacheControl, cacheControlRevalidate)
		w.Header().Add("Vary", "Authorization")

		if etagMatches(r.Header.Get(headerIfNoneMatch), tag) {
			w.Header().Del("Content-Type")
			w.Header().Del("Content-Length")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bw.body.Bytes())
	})
}

// etagMatches reports whether an If-None-Match header value (a comma list,
// possibly weak-prefixed or "*") matches tag.
func etagMatches(header, tag string) bool {
	if header == "" {
		return false
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimPrefix(strings.TrimSpace(part), "W/")
		if part == "*" || part == tag {
			return true
		}
	}
	return false
}
