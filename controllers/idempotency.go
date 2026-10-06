package controllers

import (
	"net/http"
	"strings"
)

// idempotencyKeyHeader is the optional request header that makes a legacy
// settlement endpoint safe to replay.
const idempotencyKeyHeader = "Idempotency-Key"

// maxIdempotencyKeyLen bounds a client-supplied idempotency key.
const maxIdempotencyKeyLen = 255

// parseIdempotencyKey returns the trimmed key ("" when absent) and false when
// the supplied key is longer than maxIdempotencyKeyLen.
func parseIdempotencyKey(raw string) (string, bool) {
	key := strings.TrimSpace(raw)
	return key, len(key) <= maxIdempotencyKeyLen
}

// idempotencyKeyFromRequest reads the Idempotency-Key header, answering 400
// and returning ok=false when it is over-long.
func idempotencyKeyFromRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	key, ok := parseIdempotencyKey(r.Header.Get(idempotencyKeyHeader))
	if !ok {
		fail(w, http.StatusBadRequest, "Idempotency-Key must be at most 255 characters.")
		return "", false
	}
	return key, true
}
