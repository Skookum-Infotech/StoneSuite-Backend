package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaxBody(t *testing.T) {
	const limit = 10
	exemptPath := "/upload"
	exempt := func(r *http.Request) bool { return r.URL.Path == exemptPath }

	tests := []struct {
		name       string
		limit      int64
		path       string
		body       string
		chunked    bool // hide Content-Length so only the reader can enforce the cap
		wantStatus int
		wantRead   bool // handler read the whole body without error
	}{
		{"under cap", limit, "/x", "small", false, http.StatusOK, true},
		{"exactly the cap", limit, "/x", strings.Repeat("a", limit), false, http.StatusOK, true},
		{"declared over cap is refused up front", limit, "/x", strings.Repeat("a", limit+1), false, http.StatusRequestEntityTooLarge, false},
		{"undeclared over cap fails the read", limit, "/x", strings.Repeat("a", limit+1), true, http.StatusOK, false},
		{"exempt route is uncapped", limit, exemptPath, strings.Repeat("a", limit*3), false, http.StatusOK, true},
		{"zero limit disables the cap", 0, "/x", strings.Repeat("a", limit*3), false, http.StatusOK, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			readOK := false
			h := MaxBody(tt.limit, exempt, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, err := io.ReadAll(r.Body)
				readOK = err == nil
			}))
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			if tt.chunked {
				req.ContentLength = -1
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, tt.wantRead, readOK)
			if tt.wantStatus == http.StatusRequestEntityTooLarge {
				assert.Contains(t, rec.Body.String(), `"success":false`)
			}
		})
	}
}
