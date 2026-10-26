package main

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// fakeStopper records that Stop ran, after optionally blocking on release.
type fakeStopper struct {
	release <-chan struct{}
	stopped atomic.Bool
}

func (f *fakeStopper) Stop() {
	if f.release != nil {
		<-f.release
	}
	f.stopped.Store(true)
}

func TestStopWorkersConcurrently(t *testing.T) {
	const timeout = 200 * time.Millisecond
	tests := []struct {
		name      string
		hang      bool
		wantAllOK bool
	}{
		{name: "all workers stop", hang: false, wantAllOK: true},
		{name: "hung worker abandoned at deadline", hang: true, wantAllOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			release := make(chan struct{})
			defer close(release)
			fast := &fakeStopper{}
			slow := &fakeStopper{}
			if tc.hang {
				slow.release = release
			}
			start := time.Now()
			stopWorkersConcurrently([]namedStopper{{"fast", fast}, {"slow", slow}}, timeout)
			elapsed := time.Since(start)

			assert.True(t, fast.stopped.Load())
			assert.Equal(t, tc.wantAllOK, slow.stopped.Load())
			assert.Less(t, elapsed, timeout+time.Second, "must return by the deadline")
		})
	}
}

func TestStopWorkersConcurrently_Empty(t *testing.T) {
	stopWorkersConcurrently(nil, time.Second)
}

func TestShutdownBudgetFitsKillTimeout(t *testing.T) {
	const flyKillTimeout = 30 * time.Second
	assert.Less(t, shutdownDrainTimeout+workerStopTimeout+shutdownCleanupTimeout, flyKillTimeout)
}
