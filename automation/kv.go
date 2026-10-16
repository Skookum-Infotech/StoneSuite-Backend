package automation

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

const (
	// KVEnabledKey is the Cloudflare KV key the automation-cron Worker reads
	// before calling the backend. The value is KVOn or KVOff; a missing key
	// is treated as off by the Worker.
	KVEnabledKey = "automation_enabled"
	KVOn         = "1"
	KVOff        = "0"

	cfAPIBase     = "https://api.cloudflare.com/client/v4"
	kvHTTPTimeout = 10 * time.Second
	// maxKVErrBody caps how much of a Cloudflare error body is read into an error.
	maxKVErrBody = 512
)

// KVMirror writes the platform switch to a Cloudflare KV namespace so the
// Worker can skip calling the (scale-to-zero) backend while automation is off.
// The control-plane row stays the source of truth; KV is only a mirror.
type KVMirror struct {
	accountID   string
	apiToken    string
	namespaceID string
	baseURL     string
	http        *http.Client
}

// NewKVMirror returns nil when any of the three values is blank, which
// callers treat as "KV mirroring not configured".
func NewKVMirror(accountID, apiToken, namespaceID string) *KVMirror {
	if accountID == "" || apiToken == "" || namespaceID == "" {
		return nil
	}
	return &KVMirror{
		accountID: accountID, apiToken: apiToken, namespaceID: namespaceID,
		baseURL: cfAPIBase, http: &http.Client{Timeout: kvHTTPTimeout},
	}
}

// Configured reports whether the mirror has credentials. Safe on a nil receiver.
func (m *KVMirror) Configured() bool { return m != nil }

// Set writes KVOn or KVOff to KVEnabledKey. The token is never included in
// returned errors.
func (m *KVMirror) Set(ctx context.Context, enabled bool) error {
	if m == nil {
		return fmt.Errorf("cloudflare kv mirror not configured")
	}
	val := KVOff
	if enabled {
		val = KVOn
	}
	endpoint := fmt.Sprintf("%s/accounts/%s/storage/kv/namespaces/%s/values/%s",
		m.baseURL, url.PathEscape(m.accountID), url.PathEscape(m.namespaceID), url.PathEscape(KVEnabledKey))
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader([]byte(val)))
	if err != nil {
		return fmt.Errorf("build kv request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+m.apiToken)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := m.http.Do(req)
	if err != nil {
		return fmt.Errorf("write kv value: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			slog.Warn("close cloudflare kv response body", "err", cerr)
		}
	}()
	if resp.StatusCode/100 != 2 {
		snippet, rerr := io.ReadAll(io.LimitReader(resp.Body, maxKVErrBody))
		if rerr != nil {
			return fmt.Errorf("write kv value: cloudflare returned %d (response unreadable: %v)", resp.StatusCode, rerr)
		}
		return fmt.Errorf("write kv value: cloudflare returned %d: %s", resp.StatusCode, bytes.TrimSpace(snippet))
	}
	return nil
}
