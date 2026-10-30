package controllers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// maxFormBodyBytes caps a document form's JSON body (header plus lines).
const maxFormBodyBytes = 1 << 20

// readJSONBody reads a bounded request body, decodes it into dst and returns
// the raw bytes (for idempotency.HashRequest). It answers 413/400 itself and
// returns ok=false on any failure.
func readJSONBody(w http.ResponseWriter, r *http.Request, dst any) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFormBodyBytes))
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		fail(w, http.StatusRequestEntityTooLarge, "Request body is too large.")
		return nil, false
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return nil, false
	}
	if err := json.NewDecoder(bytes.NewReader(raw)).Decode(dst); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return nil, false
	}
	return raw, true
}
