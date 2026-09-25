package services

import (
	"context"
	"encoding/json"
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
// If the Machines metadata API ever fails (token lacking permission, or a
// Machine that can't take metadata), OllamaLease permanently falls back to
// behaving as if no lease system existed: Acquire becomes a no-op and
// ReleaseAndMaybeStop degrades to a plain, unconditional StopAll — the same
// behavior this type replaces. A single warning is logged the first time
// that happens.
type OllamaLease struct {
	lifecycle *OllamaLifecycle
	key       string

	mu       sync.Mutex
	degraded bool
	warnOnce sync.Once
}

// NewOllamaLease builds a lease for holder (typically config.AppConfig.AppName)
// against the given lifecycle's Fly app.
func NewOllamaLease(lifecycle *OllamaLifecycle, holder string) *OllamaLease {
	return &OllamaLease{lifecycle: lifecycle, key: leaseKeyPrefix + sanitizeLeaseHolder(holder)}
}

// markDegraded permanently disables the lease's metadata calls after the
// first failure and logs exactly one warning explaining the fallback.
func (l *OllamaLease) markDegraded(err error) {
	l.mu.Lock()
	l.degraded = true
	l.mu.Unlock()
	l.warnOnce.Do(func() {
		slog.Warn("ollama-lease: metadata API unavailable, falling back to plain start/stop",
			"app", l.lifecycle.appName, "error", err)
	})
}

// isDegraded reports whether a prior metadata-API failure has disabled
// leasing for the rest of this process's life.
func (l *OllamaLease) isDegraded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.degraded
}

// Acquire (re)writes this holder's lease, with a fresh leaseTTL expiry, onto
// every Machine in the app. A no-op once the lease has degraded (see
// OllamaLease doc). Never returns an error the caller must act on — a
// failure here only marks the lease degraded and is logged once; callers
// (RenewLoop, the platform-toggle listener) proceed exactly as if it
// succeeded, since the fallback is "start/stop unconditionally", not "fail
// the operation".
func (l *OllamaLease) Acquire(ctx context.Context) error {
	if l.isDegraded() {
		return nil
	}
	machines, err := l.lifecycle.listMachines(ctx)
	if err != nil {
		l.markDegraded(fmt.Errorf("list machines: %w", err))
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
		l.markDegraded(firstErr)
	}
	return nil
}

// RenewLoop acquires the lease immediately and then every leaseRenewInterval
// until ctx is cancelled — the explicit exit strategy is ctx.Done(). Callers
// (the platform-toggle listener) run this in a goroutine derived from the
// shutdown context, started only while the platform AI switch is on.
func (l *OllamaLease) RenewLoop(ctx context.Context) {
	_ = l.Acquire(ctx)
	ticker := time.NewTicker(leaseRenewInterval)
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
// still unexpired; otherwise it logs and leaves the app running. Degrades to
// a plain, unconditional StopAll if the lease has degraded or any metadata
// call in this method fails — never blocks shutdown past ctx's own deadline.
func (l *OllamaLease) ReleaseAndMaybeStop(ctx context.Context) error {
	if l.isDegraded() {
		return l.lifecycle.StopAll(ctx)
	}
	machines, err := l.lifecycle.listMachines(ctx)
	if err != nil {
		l.markDegraded(fmt.Errorf("list machines: %w", err))
		return l.lifecycle.StopAll(ctx)
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
		l.markDegraded(fmt.Errorf("read lease metadata: %w", err))
		return l.lifecycle.StopAll(ctx)
	}
	if shouldStop(leases, l.key, time.Now()) {
		return l.lifecycle.StopAll(ctx)
	}
	slog.Info("ollama left running for other holder(s)", "app", l.lifecycle.appName)
	return nil
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
