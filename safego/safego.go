// Package safego contains panics in background and fan-out goroutines.
//
// middleware.Recover only covers the HTTP handler's own goroutine; a panic in
// any goroutine it (or main) spawns kills the whole process — every tenant's
// in-flight requests with it. Do and Go convert such a panic into an error,
// logged with its stack, counted in metrics and reported to Sentry.
package safego

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/getsentry/sentry-go"

	"stonesuite-backend/metrics"
)

// sentryFlushTimeout bounds the synchronous Sentry flush after a panic; panics
// are rare, and scale-to-zero may stop the machine shortly after one.
const sentryFlushTimeout = 2 * time.Second

// PanicError is the error Do returns when fn panicked.
type PanicError struct {
	Name  string
	Value any
}

// Error implements error.
func (e *PanicError) Error() string {
	return fmt.Sprintf("%s: panic: %v", e.Name, e.Value)
}

// Do runs fn, returning a *PanicError (after reporting it) if fn panicked.
// name identifies the call site in logs and metrics; keep it low-cardinality.
func Do(ctx context.Context, name string, fn func()) (err error) {
	defer func() {
		rec := recover()
		if rec == nil {
			return
		}
		report(ctx, name, rec)
		err = &PanicError{Name: name, Value: rec}
	}()
	fn()
	return nil
}

// Go runs fn on a new goroutine under Do. The caller still owns the exit
// strategy (WaitGroup/ctx) — Go only guarantees a panic can't escape.
func Go(ctx context.Context, name string, fn func()) {
	go func() {
		_ = Do(ctx, name, fn) // already reported inside Do
	}()
}

func report(ctx context.Context, name string, rec any) {
	slog.Error("goroutine panic recovered",
		slog.String("goroutine", name),
		slog.Any("panic", rec),
		slog.String("stack", string(debug.Stack())),
	)
	metrics.GoroutinePanic(name)
	if hub := sentry.CurrentHub(); hub.Client() != nil {
		local := hub.Clone()
		local.ConfigureScope(func(scope *sentry.Scope) {
			scope.SetTag("goroutine", name)
		})
		local.RecoverWithContext(ctx, rec)
		local.Flush(sentryFlushTimeout)
	}
}
