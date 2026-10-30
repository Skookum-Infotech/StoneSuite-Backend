package main

import (
	"context"
	"sort"
	"time"
)

// ragDrainIdleThreshold is how many consecutive empty drains the worker
// tolerates at ragDrainInterval before it starts backing off.
const ragDrainIdleThreshold = 20

// ragDrainIdleMax caps the idle backoff of the drain loop.
const ragDrainIdleMax = 60 * time.Second

// ragDrainNextInterval is the pure wait calculation for the drain loop. Embedder
// failures (consecutiveFailures > 0) win via ragDrainBackoff; otherwise the
// interval is ragDrainInterval until consecutiveEmpty reaches
// ragDrainIdleThreshold, then doubles with each further empty drain, capped at
// ragDrainIdleMax.
func ragDrainNextInterval(consecutiveFailures, consecutiveEmpty int) time.Duration {
	if consecutiveFailures > 0 {
		return ragDrainBackoff(consecutiveFailures)
	}
	if consecutiveEmpty < ragDrainIdleThreshold {
		return ragDrainInterval
	}
	d := ragDrainInterval
	for i := ragDrainIdleThreshold; i <= consecutiveEmpty; i++ {
		d *= 2
		if d >= ragDrainIdleMax {
			return ragDrainIdleMax
		}
	}
	return d
}

// staleStarted returns the ids in started that are absent from live (or marked
// not live), i.e. tenants whose loops must be cancelled. Sorted for determinism.
func staleStarted(started map[string]context.CancelFunc, live map[string]bool) []string {
	var stale []string
	for id := range started {
		if !live[id] {
			stale = append(stale, id)
		}
	}
	sort.Strings(stale)
	return stale
}

// sendSignal does a non-blocking send on a buffered signal channel; nil is a no-op.
func sendSignal(ch chan struct{}) {
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// registerWake returns tenantID's drain-worker wake channel, creating it
// (buffered 1, so a wake sent before the worker is ready is not lost).
func (c *IndexCoordinator) registerWake(tenantID string) <-chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.wakes[tenantID]
	if !ok {
		ch = make(chan struct{}, 1)
		c.wakes[tenantID] = ch
	}
	return ch
}

// Wake signals one tenant's drain worker to drain now and reset its idle
// backoff. A no-op for an unregistered tenant; non-blocking.
func (c *IndexCoordinator) Wake(tenantID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	ch := c.wakes[tenantID]
	c.mu.Unlock()
	sendSignal(ch)
}

// unregister removes a tenant's nudge and wake channels once its loops have
// been cancelled. Nil-safe.
func (c *IndexCoordinator) unregister(tenantID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.nudges, tenantID)
	delete(c.wakes, tenantID)
	c.mu.Unlock()
}
