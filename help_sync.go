package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Backoff bounds for retrying the app-help corpus sync: the first retry waits
// helpSyncInitialBackoff, each further failure doubles it up to
// helpSyncMaxBackoff. Every run of runUntilSynced starts back at the initial
// value, which is how the backoff resets after a success.
const (
	helpSyncInitialBackoff = 30 * time.Second
	helpSyncMaxBackoff     = 10 * time.Minute
)

// nextHelpSyncBackoff returns the wait after cur: the initial backoff when cur
// is not yet set, otherwise cur doubled and capped at helpSyncMaxBackoff.
func nextHelpSyncBackoff(cur time.Duration) time.Duration {
	if cur <= 0 {
		return helpSyncInitialBackoff
	}
	next := cur * 2
	if next > helpSyncMaxBackoff {
		return helpSyncMaxBackoff
	}
	return next
}

// helpSyncer retries a help-corpus sync with exponential backoff until it
// succeeds. Attempts are serialized by mu, so a boot-time run and an
// enable-triggered run never ingest concurrently; the second one simply finds
// the corpus up to date.
type helpSyncer struct {
	mu   sync.Mutex
	run  func(ctx context.Context) error
	wait func(ctx context.Context, d time.Duration) bool
}

// newHelpSyncer builds a syncer around run (nil error = synced or already up
// to date) using real timers.
func newHelpSyncer(run func(ctx context.Context) error) *helpSyncer {
	return &helpSyncer{run: run, wait: sleepCtx}
}

// sleepCtx waits d or until ctx is done, reporting whether the full wait elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// runUntilSynced attempts the sync, retrying with backoff after each failure,
// until it succeeds or ctx is cancelled (its exit strategy).
func (h *helpSyncer) runUntilSynced(ctx context.Context) {
	var backoff time.Duration
	for {
		if ctx.Err() != nil {
			return
		}
		h.mu.Lock()
		err := h.run(ctx)
		h.mu.Unlock()
		if err == nil {
			return
		}
		backoff = nextHelpSyncBackoff(backoff)
		slog.Warn("help corpus sync failed; will retry", "error", err, "retry_in", backoff)
		if !h.wait(ctx, backoff) {
			return
		}
	}
}
