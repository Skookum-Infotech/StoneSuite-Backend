package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Shutdown budget. The three phases run back to back and must sum to less
// than fly.toml's kill_timeout (30s), after which Fly sends SIGKILL.
const (
	// shutdownDrainTimeout bounds draining in-flight HTTP requests.
	shutdownDrainTimeout = 10 * time.Second
	// workerStopTimeout is the single deadline for stopping all background workers.
	workerStopTimeout = 12 * time.Second
	// shutdownCleanupTimeout bounds the Ollama lease release after the workers stop.
	shutdownCleanupTimeout = 5 * time.Second
)

// stopper is a background worker that blocks in Stop until it has exited.
type stopper interface{ Stop() }

// namedStopper pairs a worker with the name used in shutdown logs.
type namedStopper struct {
	name string
	s    stopper
}

// stopWorkersConcurrently stops every worker in parallel and waits at most
// timeout for all of them. A worker still running at the deadline is logged
// and abandoned: the process is exiting, which ends its goroutine.
func stopWorkersConcurrently(workers []namedStopper, timeout time.Duration) {
	var wg sync.WaitGroup
	for _, w := range workers {
		wg.Add(1)
		go func(w namedStopper) {
			defer wg.Done()
			w.s.Stop()
			slog.Info("shutdown: worker stopped", "worker", w.name)
		}(w)
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	select {
	case <-done:
	case <-ctx.Done():
		slog.Error("shutdown: workers did not stop before deadline", "timeout", timeout)
	}
}
