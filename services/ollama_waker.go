package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	wakePollInterval = 2 * time.Second
	// wakeMaxWait bounds one start-and-wait attempt: a Machine start plus DNS
	// registration is normally under 15s; the model itself loads lazily on the
	// first inference, so readiness here is just "the server answers".
	wakeMaxWait = 60 * time.Second
	// wakeCooldown stops a broken lifecycle (bad token, app destroyed) from
	// turning every failed request into a Machines API call.
	wakeCooldown = 15 * time.Second
	wakeProbeTTL = 5 * time.Second
)

// ErrWakeCooldown is returned while a recent wake attempt has failed and the
// next one is not allowed yet.
var ErrWakeCooldown = errors.New("ollama wake attempt failed recently")

// ErrWakeDisabled is returned by EnsureRunning while the platform AI switch
// is off (see SetEnabled) — the assistant is already gated on that switch
// before any request reaches the waker, so this is a defense-in-depth
// backstop, not the primary gate.
var ErrWakeDisabled = errors.New("ollama waker disabled by platform switch")

// machineStarter is the point-of-use view of OllamaLifecycle.
type machineStarter interface {
	StartAll(ctx context.Context) error
}

// OllamaWaker starts the self-hosted Ollama Machines on demand and waits until
// the server answers. Ollama is otherwise only started at backend boot, and
// the Fly app is shared, so any backend's shutdown (or an idle stop) leaves it
// down until a backend reboots. Concurrent callers share one attempt.
type OllamaWaker struct {
	starter   machineStarter
	probeURL  string
	client    *http.Client
	pollEvery time.Duration
	maxWait   time.Duration
	cooldown  time.Duration

	mu         sync.Mutex
	inflight   *wakeAttempt
	lastFailed time.Time
	enabled    bool
}

type wakeAttempt struct {
	done chan struct{}
	err  error
}

// NewOllamaWaker builds a waker that probes baseURL's /api/tags for readiness.
func NewOllamaWaker(starter machineStarter, baseURL string) *OllamaWaker {
	return &OllamaWaker{
		starter:   starter,
		probeURL:  strings.TrimRight(baseURL, "/") + "/api/tags",
		client:    &http.Client{Timeout: wakeProbeTTL},
		pollEvery: wakePollInterval,
		maxWait:   wakeMaxWait,
		cooldown:  wakeCooldown,
		enabled:   true,
	}
}

// SetEnabled turns the waker on or off, mirroring the platform AI switch:
// disabled means EnsureRunning refuses immediately with ErrWakeDisabled
// instead of ever calling StartAll. Safe to call concurrently with
// EnsureRunning.
func (w *OllamaWaker) SetEnabled(enabled bool) {
	w.mu.Lock()
	w.enabled = enabled
	w.mu.Unlock()
}

// EnsureRunning starts Ollama and returns once it answers, or an error if that
// did not happen in time. Callers arriving during an attempt wait on the same
// one; a caller's own cancellation stops its wait, not the shared attempt.
func (w *OllamaWaker) EnsureRunning(ctx context.Context) error {
	w.mu.Lock()
	if !w.enabled {
		w.mu.Unlock()
		return ErrWakeDisabled
	}
	if a := w.inflight; a != nil {
		w.mu.Unlock()
		return a.wait(ctx)
	}
	if !w.lastFailed.IsZero() && time.Since(w.lastFailed) < w.cooldown {
		w.mu.Unlock()
		return ErrWakeCooldown
	}
	a := &wakeAttempt{done: make(chan struct{})}
	w.inflight = a
	w.mu.Unlock()

	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), w.maxWait)
		defer cancel()
		err := w.startAndWait(runCtx)

		w.mu.Lock()
		a.err = err
		if err != nil {
			w.lastFailed = time.Now()
		} else {
			w.lastFailed = time.Time{}
		}
		w.inflight = nil
		w.mu.Unlock()
		close(a.done)
	}()
	return a.wait(ctx)
}

func (a *wakeAttempt) wait(ctx context.Context) error {
	select {
	case <-a.done:
		return a.err
	case <-ctx.Done():
		return fmt.Errorf("waiting for ollama: %w", ctx.Err())
	}
}

// startAndWait asks for the Machines to start, then polls until the server
// answers. A start error is not fatal on its own — another instance may have
// started them a moment ago — so readiness is the deciding signal.
func (w *OllamaWaker) startAndWait(ctx context.Context) error {
	startErr := w.starter.StartAll(ctx)
	ticker := time.NewTicker(w.pollEvery)
	defer ticker.Stop()
	for {
		if w.probe(ctx) == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			if startErr != nil {
				return fmt.Errorf("ollama not ready (start: %v): %w", startErr, ctx.Err())
			}
			return fmt.Errorf("ollama not ready: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (w *OllamaWaker) probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.probeURL, nil)
	if err != nil {
		return fmt.Errorf("new probe request: %w", err)
	}
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("probe ollama: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("probe ollama: status %d", resp.StatusCode)
	}
	return nil
}
