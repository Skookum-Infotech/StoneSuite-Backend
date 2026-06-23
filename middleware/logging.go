package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/metrics"
)

// RequestIDContextKey holds the per-request correlation id injected by
// RequestLogger and surfaced to clients via the X-Request-ID response header.
const RequestIDContextKey contextKey = "requestID"

// RequestIDHeader is the response header carrying the per-request correlation id.
const RequestIDHeader = "X-Request-ID"

// newRequestID returns a random 16-hex-character correlation id. On the
// (effectively impossible) failure of the system RNG it falls back to a
// timestamp-derived id so a request is never left without one.
func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "ts-" + time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b)
}

// RequestIDFromContext returns the correlation id for the in-flight request,
// or "" when called outside a RequestLogger-wrapped chain.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(RequestIDContextKey).(string); ok {
		return id
	}
	return ""
}

// statusRecorder wraps http.ResponseWriter to capture the status code and the
// number of bytes written, so the request logger can report them after the
// handler returns. Defaults to 200 when the handler never calls WriteHeader.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		// Mirror net/http: the first Write implies a 200 status.
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Status returns the response status code seen by the recorder (200 default).
func (r *statusRecorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// RequestLogger assigns each request a correlation id, exposes it on the
// request context and the X-Request-ID response header, and emits one
// structured slog line per request with method, path, status, latency, and —
// when the request is authenticated — the tenant and identity it ran as.
//
// It is the OUTERMOST middleware so it can observe every response, including
// those produced by the panic-recovery layer it should wrap (Recover).
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqID := newRequestID()

		ctx := context.WithValue(r.Context(), RequestIDContextKey, reqID)
		w.Header().Set(RequestIDHeader, reqID)

		rec := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(rec, r.WithContext(ctx))

		// Record Prometheus metrics from the same single point that logs the
		// request, so counts and the log line can never disagree. The route is
		// normalized to bound label cardinality (ids → {id}).
		elapsed := time.Since(start)
		metrics.Observe(r.Method, metrics.NormalizeRoute(r.URL.Path), rec.Status(), elapsed.Seconds())

		attrs := []any{
			slog.String("request_id", reqID),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", rec.Status()),
			slog.Int("bytes", rec.bytes),
			slog.Int64("latency_ms", elapsed.Milliseconds()),
			slog.String("ip", ClientIP(r)),
		}
		// Attach auth context when present (set by RequireAuth). Unauthenticated
		// requests — login, onboarding, preflight — simply omit these fields.
		if payload, err := GetUserFromContext(ctx); err == nil {
			if payload.TenantID != "" {
				attrs = append(attrs, slog.String("tenant_id", payload.TenantID))
			}
			if payload.ID != "" {
				attrs = append(attrs, slog.String("identity_id", payload.ID))
			}
		}

		msg := "request"
		switch {
		case rec.Status() >= 500:
			slog.LogAttrs(ctx, slog.LevelError, msg, toLogAttrs(attrs)...)
		case rec.Status() >= 400:
			slog.LogAttrs(ctx, slog.LevelWarn, msg, toLogAttrs(attrs)...)
		default:
			slog.LogAttrs(ctx, slog.LevelInfo, msg, toLogAttrs(attrs)...)
		}
	})
}

// toLogAttrs converts the []any built above into the []slog.Attr that
// slog.LogAttrs expects. Every element is constructed as a slog.Attr in
// RequestLogger, so the assertion always succeeds.
func toLogAttrs(in []any) []slog.Attr {
	out := make([]slog.Attr, 0, len(in))
	for _, a := range in {
		if attr, ok := a.(slog.Attr); ok {
			out = append(out, attr)
		}
	}
	return out
}
