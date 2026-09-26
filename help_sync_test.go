package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextHelpSyncBackoff(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"unset starts at the initial backoff", 0, helpSyncInitialBackoff},
		{"doubles", 30 * time.Second, time.Minute},
		{"doubles again", time.Minute, 2 * time.Minute},
		{"caps at the maximum", 8 * time.Minute, helpSyncMaxBackoff},
		{"stays capped", helpSyncMaxBackoff, helpSyncMaxBackoff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, nextHelpSyncBackoff(tt.in))
		})
	}
}

func TestHelpSyncer_RetriesWithBackoffUntilSuccess(t *testing.T) {
	calls := 0
	var waits []time.Duration
	h := &helpSyncer{
		run: func(context.Context) error {
			calls++
			if calls < 4 {
				return errors.New("embedder down")
			}
			return nil
		},
		wait: func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true },
	}

	h.runUntilSynced(context.Background())

	assert.Equal(t, 4, calls)
	assert.Equal(t, []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute}, waits)
}

func TestHelpSyncer_BackoffResetsOnNextRun(t *testing.T) {
	var waits []time.Duration
	fail := true
	h := &helpSyncer{
		run: func(context.Context) error {
			if fail {
				fail = false
				return errors.New("once")
			}
			return nil
		},
		wait: func(_ context.Context, d time.Duration) bool { waits = append(waits, d); return true },
	}
	h.runUntilSynced(context.Background())
	fail = true
	h.runUntilSynced(context.Background())

	assert.Equal(t, []time.Duration{helpSyncInitialBackoff, helpSyncInitialBackoff}, waits)
}

func TestHelpSyncer_StopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	h := &helpSyncer{
		run:  func(context.Context) error { calls++; return errors.New("down") },
		wait: func(ctx context.Context, _ time.Duration) bool { cancel(); return false },
	}

	h.runUntilSynced(ctx)

	require.Equal(t, 1, calls)
}
