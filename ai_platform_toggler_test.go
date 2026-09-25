package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"stonesuite-backend/services"
)

// TestAIPlatformToggler_NoLifecycleEnableNudgesIndexCoordinator: with no Fly
// lifecycle configured (local dev), enabling the platform switch only nudges
// every registered tenant's maintenance loop to catch up.
func TestAIPlatformToggler_NoLifecycleEnableNudgesIndexCoordinator(t *testing.T) {
	coordinator := NewIndexCoordinator()
	nudge := coordinator.register("tenant-1")
	toggler := newAIPlatformToggler(nil, nil, nil, coordinator, nil, context.Background())

	toggler.OnPlatformToggle(context.Background(), true)

	select {
	case <-nudge:
	case <-time.After(time.Second):
		t.Fatal("expected a catch-up nudge for the registered tenant")
	}
}

// TestAIPlatformToggler_NoLifecycleDisableDoesNotNudge: disabling never
// nudges — the index loops pause on their own via the settings gate.
func TestAIPlatformToggler_NoLifecycleDisableDoesNotNudge(t *testing.T) {
	coordinator := NewIndexCoordinator()
	nudge := coordinator.register("tenant-1")
	toggler := newAIPlatformToggler(nil, nil, nil, coordinator, nil, context.Background())

	toggler.OnPlatformToggle(context.Background(), false)

	select {
	case <-nudge:
		t.Fatal("disable must not nudge catch-up")
	default:
	}
}

// TestAIPlatformToggler_DisableMarksWakerDisabled: the waker refuses to start
// Ollama on demand while the platform switch is off.
func TestAIPlatformToggler_DisableMarksWakerDisabled(t *testing.T) {
	waker := services.NewOllamaWaker(services.NewOllamaLifecycle("app", "tok"), "http://127.0.0.1:0")
	toggler := newAIPlatformToggler(nil, nil, waker, NewIndexCoordinator(), nil, context.Background())

	toggler.OnPlatformToggle(context.Background(), false)
	require.ErrorIs(t, waker.EnsureRunning(context.Background()), services.ErrWakeDisabled)
}
