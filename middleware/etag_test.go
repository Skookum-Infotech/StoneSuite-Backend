package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEtagMatches(t *testing.T) {
	tests := []struct {
		name, header, tag string
		want              bool
	}{
		{"empty header", "", `"a"`, false},
		{"exact", `"a"`, `"a"`, true},
		{"weak prefix", `W/"a"`, `"a"`, true},
		{"list", `"x", "a"`, `"a"`, true},
		{"star", `*`, `"a"`, true},
		{"mismatch", `"b"`, `"a"`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, etagMatches(tc.header, tc.tag))
		})
	}
}

func TestETag(t *testing.T) {
	h := ETag(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("fail") != "" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"success":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	first := httptest.NewRecorder()
	h.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/x", nil))
	assert.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, `{"ok":true}`, first.Body.String())
	assert.Equal(t, "private, no-cache", first.Header().Get("Cache-Control"))
	tag := first.Header().Get("ETag")
	assert.NotEmpty(t, tag)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("If-None-Match", tag)
	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	assert.Equal(t, http.StatusNotModified, second.Code)
	assert.Empty(t, second.Body.String())

	errRec := httptest.NewRecorder()
	h.ServeHTTP(errRec, httptest.NewRequest(http.MethodGet, "/x?fail=1", nil))
	assert.Equal(t, http.StatusNotFound, errRec.Code)
	assert.Empty(t, errRec.Header().Get("ETag"))

	post := httptest.NewRecorder()
	h.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/x", nil))
	assert.Empty(t, post.Header().Get("ETag"))
}
