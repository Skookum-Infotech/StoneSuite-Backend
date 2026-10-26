package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// Deadline gives every request context a timeout of d. http.Server's
// WriteTimeout closes the connection but never cancels the request context, so
// before this a hung query or outbound call kept running (and kept its pool
// connection) long after the client was gone. d must be below WriteTimeout so
// handlers can still answer with an error.
//
// Requests whose path matches an exempt prefix or suffix run without it:
// streaming responses bound themselves, and the internal automation tick has
// its own per-tenant timeouts. A non-positive d disables the middleware.
func Deadline(d time.Duration, exemptPrefixes, exemptSuffixes []string, next http.Handler) http.Handler {
	if d <= 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if deadlineExempt(r.URL.Path, exemptPrefixes, exemptSuffixes) {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), d)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// deadlineExempt reports whether path matches any exempt prefix or suffix.
func deadlineExempt(path string, prefixes, suffixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	for _, s := range suffixes {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}
