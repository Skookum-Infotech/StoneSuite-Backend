package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"

	"stonesuite-backend/models"
)

// Recover is HTTP middleware that turns a handler panic into a clean 500 JSON
// response instead of crashing the process. On a single-VM deployment a single
// unrecovered panic takes the whole server — and therefore every tenant —
// offline, so this is a hard availability requirement, not a nicety.
//
// It logs the panic value and full stack trace via slog at ERROR level,
// correlated by the request id set by RequestLogger, then returns a generic
// message to the client (never the panic detail or stack, which can leak
// internals). It MUST run inside RequestLogger so the request is still logged
// with the resulting 500 status.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// http.ErrAbortHandler is the sanctioned way for a handler to abort
			// the connection; re-panic so net/http handles it as intended.
			if rec == http.ErrAbortHandler {
				panic(rec)
			}

			reqID := RequestIDFromContext(r.Context())
			slog.Error("panic recovered",
				slog.String("request_id", reqID),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Any("panic", rec),
				slog.String("stack", string(debug.Stack())),
			)

			// Best-effort clean error. If the handler already started writing the
			// response body, the status is locked in and WriteHeader is a no-op —
			// we still avoid crashing, which is the point.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "An unexpected error occurred. Please try again.",
			})
		}()

		next.ServeHTTP(w, r)
	})
}
