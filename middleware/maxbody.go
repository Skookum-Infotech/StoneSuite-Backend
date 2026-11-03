package middleware

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/models"
)

// msgBodyTooLarge is the 413 body for a request whose declared size is over the cap.
const msgBodyTooLarge = "Request body is too large."

// MaxBody caps every request body at limit bytes so no handler, including the
// unauthenticated ones, can be made to buffer an arbitrarily large payload.
// A declared Content-Length over the cap is refused up front with 413; an
// undeclared (chunked) body is cut off at the cap and the handler's read
// fails, which it already reports as a bad request.
//
// exempt names the few routes that legitimately take more (file uploads) and
// enforce their own, larger cap in the handler: a global reader would sit
// underneath theirs and win, since the smaller of two nested limits applies.
// limit <= 0 disables the cap.
func MaxBody(limit int64, exempt func(*http.Request) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limit <= 0 || r.Body == nil || r.Body == http.NoBody || (exempt != nil && exempt(r)) {
			next.ServeHTTP(w, r)
			return
		}
		if r.ContentLength > limit {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: msgBodyTooLarge})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}
