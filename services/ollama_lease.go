package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// leaseKeyPrefix namespaces every lease key this backend writes to a Machine's
// metadata, so ReleaseAndMaybeStop can tell its own key apart from anything
// else that might be stored there.
const leaseKeyPrefix = "ss-lease-"

// leaseTTL is how long a lease is considered valid after it was last
// (re)acquired. leaseRenewInterval must be comfortably shorter than this so a
// single missed renewal doesn't let the lease expire.
const (
	leaseTTL           = 15 * time.Minute
	leaseRenewInterval = 5 * time.Minute
)

// leaseDegradeAfterFailures is how many consecutive metadata-API failures it
// takes to mark the lease degraded. One transient blip must not disable
// leasing; any success resets the count.
const leaseDegradeAfterFailures = 3

// sanitizeLeaseHolder reduces holder to the [a-z0-9-] alphabet the lease key
// is built from — Fly app names are already this shape, but this is the
// point where an unexpected value (a stray env var in local dev) is made
// safe rather than trusted.
func sanitizeLeaseHolder(holder string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(holder) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = "app"
	}
	return s
}

// OllamaLease coordinates the shared Ollama app's on/off state across
// multiple backend deployments (dev and prod share one Fly app): each holder
// writes its own "ss-lease-<holder>" key, with the current unix-seconds
// expiry as its value, to every Machine's metadata. A holder only stops the
// app when no other holder's lease is still unexpired (see shouldStop) — so
// one environment turning its AI assistant off never stops the box the other
// environment is actively using.
//
// If the Machines metadata API fails leaseDegradeAfterFailures times in a row
// (token lacking permission, or a Machine that can't take metadata), the lease
// is marked degraded: OtherHolders reports nothing and ReleaseAndMaybeStop
// cannot tell whether another holder is using the box. A degraded lease keeps
// retrying Acquire on every RenewLoop tick, and the first success clears the
// degradation. While degraded, a process that previously held a lease leaves
// the shared box running rather than risk stopping the other environment's
// Ollama; a process that never held one falls back to a plain StopAll.
type OllamaLease struct {
	lifecycle *OllamaLifecycle
	key       string

	mu       sync.Mutex
	failures int  // consecutive metadata failures
	degraded bool // failures reached leaseDegradeAfterFailures
	held     bool // this process has successfully written its lease
}

// NewOllamaLease builds a lease for holder (typically config.AppConfig.AppName)
// against the given lifecycle's Fly app.
func NewOllamaLease(lifecycle *OllamaLifecycle, holder string) *OllamaLease {
	return &OllamaLease{lifecycle: lifecycle, key: leaseKeyPrefix + sanitizeLeaseHolder(holder)}
}

// recordFailure counts one consecutive metadata failure and, on reaching
// leaseDegradeAfterFailures, marks the lease degraded (warning once per
// transition into that state).
func (l *OllamaLease) recordFailure(err error) {
	l.mu.Lock()
	l.failures++
	becameDegraded := !l.degraded && l.failures >= leaseDegradeAfterFailures
	if becameDegraded {
		l.degraded = true
	}
	failures := l.failures
	l.mu.Unlock()
	if becameDegraded {
		slog.Warn("ollama-lease: metadata API unavailable, lease degraded",
			"app", l.lifecycle.appName, "consecutive_failures", failures, "error", err)
	}
}

// recordSuccess resets the failure count and clears a degraded state.
func (l *OllamaLease) recordSuccess() {
	l.mu.Lock()
	wasDegraded := l.degraded
	l.failures = 0
	l.degraded = false
	l.mu.Unlock()
	if wasDegraded {
		slog.Info("ollama-lease: metadata API recovered, lease no longer degraded", "app", l.lifecycle.appName)
	}
}

// isDegraded reports whether enough consecutive metadata failures have
// accumulated to consider leasing unavailable right now.
func (l *OllamaLease) isDegraded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.degraded
}

// hasHeld reports whether this process has successfully written its lease.
func (l *OllamaLease) hasHeld() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held
}

// setHeld records whether this process currently has a lease written.
func (l *OllamaLease) setHeld(held bool) {
	l.mu.Lock()
	l.held = held
	l.mu.Unlock()
}

// Acquire (re)writes this holder's lease, with a fresh leaseTTL expiry, onto
// every Machine in the app. It always attempts the write, even while degraded,
// so a recovered metadata API clears the degradation. Never returns an error
// the caller must act on — a failure only counts toward degradation and is
// logged when that threshold is crossed; callers (RenewLoop, the
// platform-toggle listener) proceed exactly as if it succeeded.
func (l *OllamaLease) Acquire(ctx context.Context) error {
	machines, err := l.lifecycle.listMachines(ctx)
	if err != nil {
		l.recordFailure(fmt.Errorf("list machines: %w", err))
		return nil
	}
	expiry := strconv.FormatInt(time.Now().Add(leaseTTL).Unix(), 10)
	var firstErr error
	for _, m := range machines {
		if err := l.setMetadata(ctx, m.ID, l.key, expiry); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("set lease metadata on machine %s: %w", m.ID, err)
		}
	}
	if firstErr != nil {
		l.recordFailure(firstErr)
		return nil
	}
	l.recordSuccess()
	if len(machines) > 0 {
		l.setHeld(true)
	}
	return nil
}

// RenewLoop acquires the lease immediately and then every leaseRenewInterval
// until ctx is cancelled — the explicit exit strategy is ctx.Done(). Every
// tick retries, including while degraded. Callers (the platform-toggle
// listener) run this in a goroutine derived from the shutdown context, started
// only while the platform AI switch is on.
func (l *OllamaLease) RenewLoop(ctx context.Context) {
	l.renewLoop(ctx, leaseRenewInterval)
}

// renewLoop is RenewLoop with an injectable interval, for tests.
func (l *OllamaLease) renewLoop(ctx context.Context, interval time.Duration) {
	_ = l.Acquire(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = l.Acquire(ctx)
		}
	}
}

// ReleaseAndMaybeStop deletes this holder's lease key from every Machine,
// then stops the app only if shouldStop decides no other holder's lease is
// still unexpired; otherwise it logs and leaves the app running. When the
// lease is degraded, or a metadata call in this method fails, other holders
// can't be ruled out: a process that previously held a lease leaves the
// shared box running (with a warning), while one that never did falls back to
// a plain StopAll. Never blocks shutdown past ctx's own deadline.
func (l *OllamaLease) ReleaseAndMaybeStop(ctx context.Context) error {
	if l.isDegraded() {
		return l.stopUnverified(ctx, errors.New("lease degraded"))
	}
	machines, err := l.lifecycle.listMachines(ctx)
	if err != nil {
		l.recordFailure(fmt.Errorf("list machines: %w", err))
		return l.stopUnverified(ctx, err)
	}
	if len(machines) == 0 {
		return nil
	}
	for _, m := range machines {
		if err := l.deleteMetadata(ctx, m.ID, l.key); err != nil {
			slog.Warn("ollama-lease: delete lease metadata failed", "machine", m.ID, "error", err)
		}
	}
	leases, err := l.getMetadata(ctx, machines[0].ID)
	if err != nil {
		l.recordFailure(fmt.Errorf("read lease metadata: %w", err))
		return l.stopUnverified(ctx, err)
	}
	l.recordSuccess()
	l.setHeld(false) // own key deleted and verified; nothing left to protect
	if shouldStop(leases, l.key, time.Now()) {
		return l.lifecycle.StopAll(ctx)
	}
	slog.Info("ollama left running for other holder(s)", "app", l.lifecycle.appName)
	return nil
}

// stopUnverified handles a release where other holders could not be checked.
// If this process held a lease, the shared box is left running — stopping it
// could kill the other environment's Ollama — and a warning is logged;
// otherwise it stops unconditionally, as it did before leases existed.
func (l *OllamaLease) stopUnverified(ctx context.Context, cause error) error {
	if l.hasHeld() {
		slog.Warn("ollama-lease: cannot verify other holders; leaving shared Ollama running",
			"app", l.lifecycle.appName, "error", cause)
		return nil
	}
	return l.lifecycle.StopAll(ctx)
}

// OtherHolders reports the still-unexpired lease holders other than this
// one, read from the first Machine's metadata, best-effort: any error (or a
// degraded lease) yields an empty list rather than propagating, since this
// only backs an informational status field.
func (l *OllamaLease) OtherHolders(ctx context.Context) []string {
	if l.isDegraded() {
		return nil
	}
	machines, err := l.lifecycle.listMachines(ctx)
	if err != nil || len(machines) == 0 {
		return nil
	}
	leases, err := l.getMetadata(ctx, machines[0].ID)
	if err != nil {
		return nil
	}
	now := time.Now()
	var others []string
	for key, val := range leases {
		if key == l.key || !strings.HasPrefix(key, leaseKeyPrefix) {
			continue
		}
		ts, err := strconv.ParseInt(val, 10, 64)
		if err != nil || !time.Unix(ts, 0).After(now) {
			continue
		}
		others = append(others, strings.TrimPrefix(key, leaseKeyPrefix))
	}
	return others
}

// shouldStop is the pure decision ReleaseAndMaybeStop applies: stop the
// shared Machine only when no lease key other than self is still unexpired
// as of now. A malformed value (not a unix-seconds integer) is treated as no
// lease at all, not as a block. An empty map, or a map containing only self,
// always stops.
func shouldStop(leases map[string]string, self string, now time.Time) bool {
	for key, val := range leases {
		if key == self || !strings.HasPrefix(key, leaseKeyPrefix) {
			continue
		}
		ts, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			continue
		}
		if time.Unix(ts, 0).After(now) {
			return false
		}
	}
	return true
}

// metadataValue is the request body Fly's set-metadata endpoint expects.
type metadataValue struct {
	Value string `json:"value"`
}

// getMetadata reads every metadata key/value pair stored on one Machine.
func (l *OllamaLease) getMetadata(ctx context.Context, machineID string) (map[string]string, error) {
	url := fmt.Sprintf("%s/apps/%s/machines/%s/metadata", flyMachinesAPIBase, l.lifecycle.appName, machineID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+l.lifecycle.token)

	resp, err := l.lifecycle.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	metadata := map[string]string{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &metadata); err != nil {
			return nil, fmt.Errorf("decode: %w", err)
		}
	}
	return metadata, nil
}

// setMetadata writes one key/value pair to one Machine's metadata.
func (l *OllamaLease) setMetadata(ctx context.Context, machineID, key, value string) error {
	url := fmt.Sprintf("%s/apps/%s/machines/%s/metadata/%s", flyMachinesAPIBase, l.lifecycle.appName, machineID, key)
	payload, err := json.Marshal(metadataValue{Value: value})
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+l.lifecycle.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := l.lifecycle.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

// deleteMetadata removes one key from one Machine's metadata.
func (l *OllamaLease) deleteMetadata(ctx context.Context, machineID, key string) error {
	url := fmt.Sprintf("%s/apps/%s/machines/%s/metadata/%s", flyMachinesAPIBase, l.lifecycle.appName, machineID, key)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+l.lifecycle.token)

	resp, err := l.lifecycle.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}
