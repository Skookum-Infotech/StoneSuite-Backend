package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/provider/ollama"
	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
)

// aiToggleWarmupTimeout bounds the detached warmup -> index-catch-up goroutine
// OnPlatformToggle starts on enable — it must never run unbounded in the
// background. (The help-corpus sync is a separate retrying goroutine bounded
// only by the enable context; see helpSyncer.) Raised from 2 to 4 minutes to match controllers.warmBoundedTimeout
// — a cold Machine start plus Ollama's first-inference model-load latency can
// together exceed 2 minutes.
const aiToggleWarmupTimeout = 4 * time.Minute

// aiPlatformToggler implements controllers.PlatformToggleListener: it ties
// the shared Ollama Machine's lifecycle (start/stop, on-demand wake, and a
// lease so this backend never stops a Machine another deployment sharing the
// same Fly app is actively using) and the RAG index catch-up nudge to the
// platform AI switch. lifecycle/lease/waker are all nil together when
// FLY_OLLAMA_API_TOKEN is unset (local dev, or an always-on embedder box
// with no backend-driven start/stop) — OnPlatformToggle then only nudges the
// index coordinator, since there is no Ollama process to manage.
type aiPlatformToggler struct {
	lifecycle   *services.OllamaLifecycle
	lease       *services.OllamaLease
	waker       *services.OllamaWaker
	coordinator *IndexCoordinator
	cpPool      *pgxpool.Pool
	// shutdownCtx bounds the lease-renew loop's lifetime; it is not the ctx
	// OnPlatformToggle itself is called with, since a caller's own
	// per-request context must not cut the renew loop short.
	shutdownCtx context.Context

	mu          sync.Mutex
	renewCancel context.CancelFunc

	// toggleMu serializes OnPlatformToggle, so a boot-time enable, a runtime
	// PUT enable, and a disable never interleave. enableCancel (guarded by
	// toggleMu) cancels the in-flight enable's background work; a disable, or a
	// newer enable, cancels it. wg lets tests wait for those goroutines.
	toggleMu     sync.Mutex
	enableCancel context.CancelFunc
	wg           sync.WaitGroup

	// warmFn warms the embedder and chat model; a field so tests can fake it.
	warmFn func(ctx context.Context) error
	help   *helpSyncer
}

// newAIPlatformToggler builds a toggler. lifecycle, lease, and waker must all
// be nil together, or all be non-nil together — pass all nil when Ollama
// lifecycle control isn't configured (local dev).
func newAIPlatformToggler(lifecycle *services.OllamaLifecycle, lease *services.OllamaLease, waker *services.OllamaWaker, coordinator *IndexCoordinator, cpPool *pgxpool.Pool, shutdownCtx context.Context) *aiPlatformToggler {
	t := &aiPlatformToggler{
		lifecycle: lifecycle, lease: lease, waker: waker,
		coordinator: coordinator, cpPool: cpPool, shutdownCtx: shutdownCtx,
	}
	t.warmFn = warmUpModels
	t.help = newHelpSyncer(func(ctx context.Context) error { return syncHelpCorpus(ctx, t.cpPool) })
	return t
}

// warmUpModels loads the embedder and chat model into Ollama's memory.
func warmUpModels(ctx context.Context) error {
	emb := ollama.NewQueryEmbedder(config.AppConfig.OllamaBaseURL, config.AppConfig.AIEmbedModel, config.AppConfig.AIEmbedDim)
	return ragcore.WarmUp(ctx, emb, newChatClient())
}

// startHelpSync launches the retrying help-corpus sync in its own goroutine,
// bounded by ctx. Used at boot when there is no Ollama lifecycle to hang it off.
func (t *aiPlatformToggler) startHelpSync(ctx context.Context) {
	t.wg.Add(1)
	go func() {
		defer t.wg.Done()
		t.help.runUntilSynced(ctx)
	}()
}

// OnPlatformToggle implements controllers.PlatformToggleListener, called
// after a successful write to the platform AI switch. Calls are serialized;
// a disable cancels any background work an earlier enable started.
func (t *aiPlatformToggler) OnPlatformToggle(ctx context.Context, enabled bool) {
	t.toggleMu.Lock()
	defer t.toggleMu.Unlock()
	if enabled {
		t.onEnable(ctx)
		return
	}
	t.onDisable(ctx)
}

// onEnable starts the lease-renew loop and the shared Ollama Machine, then
// detaches bounded background work so the caller (the settings PUT handler)
// doesn't block on it: a warmup -> index-catch-up sequence and, in its own
// goroutine, the retrying help-corpus sync. With no lifecycle configured, the
// only action is nudging the index coordinator — there is no Ollama process to
// lease or start. Must be called with toggleMu held.
func (t *aiPlatformToggler) onEnable(ctx context.Context) {
	if t.waker != nil {
		t.waker.SetEnabled(true)
	}
	if t.lifecycle == nil {
		t.coordinator.CatchUpAll(ctx)
		return
	}

	if t.lease != nil {
		t.startRenewLoop()
	}
	if err := t.lifecycle.StartAll(ctx); err != nil {
		slog.Error("ai-platform-toggle: ollama start failed", "error", err)
	}
	t.launchEnableWork()
}

// launchEnableWork cancels any previous enable's work and starts this
// enable's, under a fresh context derived from shutdownCtx and stored so a
// later disable can cancel it. Both goroutines exit on that context's
// cancellation. Must be called with toggleMu held.
func (t *aiPlatformToggler) launchEnableWork() {
	t.cancelEnableWork()
	enableCtx, cancel := context.WithCancel(t.shutdownCtx)
	t.enableCancel = cancel

	warm, help := t.warmFn, t.help
	t.wg.Add(2)
	go func() {
		defer t.wg.Done()
		runCtx, stop := context.WithTimeout(enableCtx, aiToggleWarmupTimeout)
		defer stop()
		if err := warm(runCtx); err != nil {
			slog.Warn("ai-platform-toggle: warmup failed", "error", err)
			return
		}
		t.coordinator.CatchUpAll(runCtx)
	}()
	go func() {
		defer t.wg.Done()
		help.runUntilSynced(enableCtx)
	}()
}

// cancelEnableWork cancels the in-flight enable's background work, if any.
// Must be called with toggleMu held.
func (t *aiPlatformToggler) cancelEnableWork() {
	if t.enableCancel != nil {
		t.enableCancel()
		t.enableCancel = nil
	}
}

// onDisable (toggleMu held) cancels any in-flight enable work, marks the waker disabled (so EnsureRunning refuses instead of
// starting Ollama on demand), stops the lease-renew loop, and then releases
// this holder's lease — stopping the shared Machine only if no other holder
// (e.g. the other environment sharing the Fly app) still holds one. The RAG
// index maintenance loops already pause on their own via the AI-settings
// gate, so no explicit pause is needed here.
func (t *aiPlatformToggler) onDisable(ctx context.Context) {
	t.cancelEnableWork()
	if t.waker != nil {
		t.waker.SetEnabled(false)
	}
	if t.lifecycle == nil {
		return
	}
	t.stopRenewLoop()
	if t.lease != nil {
		if err := t.lease.ReleaseAndMaybeStop(ctx); err != nil {
			slog.Error("ai-platform-toggle: release lease / stop ollama failed", "error", err)
		}
		return
	}
	if err := t.lifecycle.StopAll(ctx); err != nil {
		slog.Error("ai-platform-toggle: ollama stop failed", "error", err)
	}
}

// startRenewLoop launches the lease-renew goroutine, derived from
// shutdownCtx so it also stops on process shutdown, unless one is already
// running. Its explicit exit strategy is renewCancel (stopRenewLoop) or
// shutdownCtx cancellation.
func (t *aiPlatformToggler) startRenewLoop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.renewCancel != nil {
		return
	}
	renewCtx, cancel := context.WithCancel(t.shutdownCtx)
	t.renewCancel = cancel
	go t.lease.RenewLoop(renewCtx)
}

// stopRenewLoop cancels a running lease-renew goroutine, if any.
func (t *aiPlatformToggler) stopRenewLoop() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.renewCancel != nil {
		t.renewCancel()
		t.renewCancel = nil
	}
}
