package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRagDrainNextInterval(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		empty    int
		want     time.Duration
	}{
		{"healthy with work", 0, 0, ragDrainInterval},
		{"below idle threshold", 0, ragDrainIdleThreshold - 1, ragDrainInterval},
		{"at idle threshold doubles once", 0, ragDrainIdleThreshold, 2 * ragDrainInterval},
		{"one past threshold", 0, ragDrainIdleThreshold + 1, 4 * ragDrainInterval},
		{"two past threshold", 0, ragDrainIdleThreshold + 2, 8 * ragDrainInterval},
		{"far idle capped", 0, ragDrainIdleThreshold + 50, ragDrainIdleMax},
		{"failures win over idle", 1, 100, ragDrainBackoffInitial},
		{"failures backoff doubles", 2, 0, 2 * ragDrainBackoffInitial},
		{"failures capped", 20, 0, ragDrainBackoffMax},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ragDrainNextInterval(tt.failures, tt.empty))
		})
	}
}

func TestStaleStarted(t *testing.T) {
	noop := context.CancelFunc(func() {})
	tests := []struct {
		name    string
		started map[string]context.CancelFunc
		live    map[string]bool
		want    []string
	}{
		{"empty", map[string]context.CancelFunc{}, map[string]bool{"a": true}, nil},
		{"all live", map[string]context.CancelFunc{"a": noop, "b": noop}, map[string]bool{"a": true, "b": true}, nil},
		{"absent from list", map[string]context.CancelFunc{"a": noop, "b": noop}, map[string]bool{"a": true}, []string{"b"}},
		{"marked not live", map[string]context.CancelFunc{"a": noop, "b": noop}, map[string]bool{"a": false, "b": true}, []string{"a"}},
		{"nil live drops all, sorted", map[string]context.CancelFunc{"b": noop, "a": noop}, nil, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, staleStarted(tt.started, tt.live))
		})
	}
}

func TestTenantChildContextStopsLoop(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	started := map[string]context.CancelFunc{}
	tenantCtx, cancel := context.WithCancel(parent)
	started["t1"] = cancel

	exited := make(chan struct{})
	go func() {
		defer close(exited)
		<-tenantCtx.Done() // fake per-tenant loop
	}()

	for _, id := range staleStarted(started, map[string]bool{}) {
		started[id]()
		delete(started, id)
	}
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("tenant loop did not stop after child ctx cancel")
	}
	assert.Empty(t, started)
	assert.NoError(t, parent.Err(), "parent ctx must stay live")
}

func TestIndexCoordinatorWakeAndUnregister(t *testing.T) {
	c := NewIndexCoordinator()
	nudge := c.register("t1")
	wake := c.registerWake("t1")

	c.CatchUp("t1")
	c.CatchUp("t1") // second is a non-blocking no-op
	assert.Len(t, nudge, 1)
	assert.Len(t, wake, 1)

	c.unregister("t1")
	c.CatchUp("t1") // unregistered: no-op, no panic
	c.Wake("t1")
	var nilC *IndexCoordinator
	nilC.unregister("t1")
	nilC.Wake("t1")
}
