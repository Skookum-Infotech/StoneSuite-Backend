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

// fakeWork is a toggler whose warmup and help sync block until their context
// is cancelled. started fires once each goroutine is inside its fake (so a
// cancel can't beat the goroutine's first check), done once it saw the cancel.
type fakeWork struct {
	toggler               *aiPlatformToggler
	warmStarted, warmDone chan struct{}
	helpStarted, helpDone chan struct{}
}

func newFakeWork() *fakeWork {
	f := &fakeWork{
		warmStarted: make(chan struct{}), warmDone: make(chan struct{}),
		helpStarted: make(chan struct{}), helpDone: make(chan struct{}),
	}
	f.toggler = newAIPlatformToggler(nil, nil, nil, NewIndexCoordinator(), nil, context.Background())
	f.toggler.warmFn = func(ctx context.Context) error {
		close(f.warmStarted)
		<-ctx.Done()
		close(f.warmDone)
		return ctx.Err()
	}
	f.toggler.help = &helpSyncer{
		run: func(ctx context.Context) error {
			close(f.helpStarted)
			<-ctx.Done()
			close(f.helpDone)
			return ctx.Err()
		},
		wait: sleepCtx,
	}
	return f
}

func waitClosed(t *testing.T, name string, ch chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

// TestAIPlatformToggler_DisableCancelsInFlightEnableWork: a disable arriving
// while an enable's warmup and help sync are still running cancels both.
func TestAIPlatformToggler_DisableCancelsInFlightEnableWork(t *testing.T) {
	f := newFakeWork()
	f.toggler.toggleMu.Lock()
	f.toggler.launchEnableWork()
	f.toggler.toggleMu.Unlock()
	waitClosed(t, "warmup start", f.warmStarted)
	waitClosed(t, "help sync start", f.helpStarted)

	f.toggler.OnPlatformToggle(context.Background(), false)

	waitClosed(t, "warmup exit", f.warmDone)
	waitClosed(t, "help sync exit", f.helpDone)
	f.toggler.wg.Wait()
}

// TestAIPlatformToggler_NewEnableReplacesPreviousWork: launching a second
// enable cancels the first one's context instead of leaking it.
func TestAIPlatformToggler_NewEnableReplacesPreviousWork(t *testing.T) {
	f := newFakeWork()
	f.toggler.toggleMu.Lock()
	f.toggler.launchEnableWork()
	f.toggler.toggleMu.Unlock()
	waitClosed(t, "warmup start", f.warmStarted)
	waitClosed(t, "help sync start", f.helpStarted)

	f.toggler.toggleMu.Lock()
	f.toggler.warmFn = func(context.Context) error { return nil }
	f.toggler.help = &helpSyncer{run: func(context.Context) error { return nil }, wait: sleepCtx}
	f.toggler.launchEnableWork()
	f.toggler.toggleMu.Unlock()

	waitClosed(t, "first warmup exit", f.warmDone)
	waitClosed(t, "first help sync exit", f.helpDone)
	f.toggler.wg.Wait()
}

// TestAIPlatformToggler_TogglesAreSerialized: OnPlatformToggle waits for the
// toggle lock, so concurrent boot/PUT toggles cannot interleave.
func TestAIPlatformToggler_TogglesAreSerialized(t *testing.T) {
	toggler := newAIPlatformToggler(nil, nil, nil, NewIndexCoordinator(), nil, context.Background())
	toggler.toggleMu.Lock()
	done := make(chan struct{})
	go func() {
		toggler.OnPlatformToggle(context.Background(), false)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("OnPlatformToggle ran while another toggle held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	toggler.toggleMu.Unlock()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnPlatformToggle did not proceed after the lock was released")
	}
}
