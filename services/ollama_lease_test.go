package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShouldStop(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	future := strconv.FormatInt(now.Add(10*time.Minute).Unix(), 10)
	past := strconv.FormatInt(now.Add(-10*time.Minute).Unix(), 10)

	tests := []struct {
		name   string
		leases map[string]string
		self   string
		want   bool
	}{
		{
			name:   "other holder has a valid unexpired lease",
			leases: map[string]string{"ss-lease-self": future, "ss-lease-other": future},
			self:   "ss-lease-self",
			want:   false,
		},
		{
			name:   "other holder's lease is expired",
			leases: map[string]string{"ss-lease-self": future, "ss-lease-other": past},
			self:   "ss-lease-self",
			want:   true,
		},
		{
			name:   "other holder's value is malformed",
			leases: map[string]string{"ss-lease-self": future, "ss-lease-other": "not-a-timestamp"},
			self:   "ss-lease-self",
			want:   true,
		},
		{
			name:   "only self holds a lease",
			leases: map[string]string{"ss-lease-self": future},
			self:   "ss-lease-self",
			want:   true,
		},
		{
			name:   "no leases at all",
			leases: map[string]string{},
			self:   "ss-lease-self",
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, shouldStop(tt.leases, tt.self, now))
		})
	}
}

func TestSanitizeLeaseHolder(t *testing.T) {
	tests := []struct{ in, want string }{
		{"stonesuite-backend-prod", "stonesuite-backend-prod"},
		{"StoneSuite_Backend Dev!", "stonesuite-backend-dev"},
		{"", "app"},
		{"---", "app"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, sanitizeLeaseHolder(tt.in), tt.in)
	}
}

// metadataServer fakes the Fly Machines metadata endpoints in-memory, keyed
// by machine id, so Acquire/ReleaseAndMaybeStop can be exercised end-to-end
// via HTTP the same way ollama_lifecycle_test.go exercises start/stop.
func metadataServer(t *testing.T, machines []flyMachine) (*httptest.Server, map[string]map[string]string) {
	t.Helper()
	store := map[string]map[string]string{}
	for _, m := range machines {
		store[m.ID] = map[string]string{}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/machines") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(machines)
		case strings.HasSuffix(r.URL.Path, "/stop") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/metadata/") && r.Method == http.MethodPost:
			id, key := metadataPathParts(r.URL.Path)
			var body metadataValue
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			store[id][key] = body.Value
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/metadata/") && r.Method == http.MethodDelete:
			id, key := metadataPathParts(r.URL.Path)
			delete(store[id], key)
			w.WriteHeader(http.StatusOK)
		case strings.HasSuffix(r.URL.Path, "/metadata") && r.Method == http.MethodGet:
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/apps/app1/machines/"), "/metadata")
			_ = json.NewEncoder(w).Encode(store[id])
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, store
}

// metadataPathParts extracts (machineID, key) from a
// /v1/apps/app1/machines/{id}/metadata/{key} path.
func metadataPathParts(path string) (string, string) {
	trimmed := strings.TrimPrefix(path, "/v1/apps/app1/machines/")
	parts := strings.SplitN(trimmed, "/metadata/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

func TestOllamaLeaseAcquireWritesExpiryToEveryMachine(t *testing.T) {
	machines := []flyMachine{{ID: "m1"}, {ID: "m2"}}
	srv, store := metadataServer(t, machines)
	overrideBase(t, srv.URL)

	lifecycle := NewOllamaLifecycle("app1", "test-token")
	lifecycle.client = srv.Client()
	lease := NewOllamaLease(lifecycle, "backend-a")

	require.NoError(t, lease.Acquire(context.Background()))

	for _, m := range machines {
		v, ok := store[m.ID][lease.key]
		require.True(t, ok, "machine %s missing lease key", m.ID)
		ts, err := strconv.ParseInt(v, 10, 64)
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now().Add(leaseTTL), time.Unix(ts, 0), 5*time.Second)
	}
}

func TestOllamaLeaseReleaseAndMaybeStop_StopsWhenNoOtherHolder(t *testing.T) {
	machines := []flyMachine{{ID: "m1"}}
	srv, store := metadataServer(t, machines)
	overrideBase(t, srv.URL)

	lifecycle := NewOllamaLifecycle("app1", "test-token")
	lifecycle.client = srv.Client()
	lease := NewOllamaLease(lifecycle, "backend-a")
	require.NoError(t, lease.Acquire(context.Background()))

	require.NoError(t, lease.ReleaseAndMaybeStop(context.Background()))

	_, ok := store["m1"][lease.key]
	assert.False(t, ok, "own lease key must be deleted")
}

func TestOllamaLeaseReleaseAndMaybeStop_LeavesRunningForOtherHolder(t *testing.T) {
	machines := []flyMachine{{ID: "m1"}}
	srv, store := metadataServer(t, machines)
	overrideBase(t, srv.URL)

	lifecycle := NewOllamaLifecycle("app1", "test-token")
	lifecycle.client = srv.Client()

	leaseA := NewOllamaLease(lifecycle, "backend-a")
	leaseB := NewOllamaLease(lifecycle, "backend-b")
	require.NoError(t, leaseA.Acquire(context.Background()))
	require.NoError(t, leaseB.Acquire(context.Background()))

	require.NoError(t, leaseA.ReleaseAndMaybeStop(context.Background()))

	_, ok := store["m1"][leaseB.key]
	assert.True(t, ok, "other holder's lease must be left alone")
}

func TestOllamaLeaseDegradesOnMetadataFailure(t *testing.T) {
	// Simulates a token that can start/stop Machines but lacks permission on
	// the metadata endpoints specifically — the fallback case the doc
	// comment describes.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/machines") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]flyMachine{{ID: "m1"}})
		case strings.HasSuffix(r.URL.Path, "/stop") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/metadata"):
			w.WriteHeader(http.StatusForbidden)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	overrideBase(t, srv.URL)

	lifecycle := NewOllamaLifecycle("app1", "test-token")
	lifecycle.client = srv.Client()
	lease := NewOllamaLease(lifecycle, "backend-a")

	require.NoError(t, lease.Acquire(context.Background()), "Acquire must not surface a metadata failure")
	assert.True(t, lease.isDegraded())

	require.NoError(t, lease.ReleaseAndMaybeStop(context.Background()), "must fall back to a plain StopAll, not error")
}
