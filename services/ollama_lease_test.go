package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

// flakyMetadataServer fakes a Fly app whose metadata endpoints can be switched
// between working and failing (403), and counts /stop calls.
type flakyMetadataServer struct {
	failing atomic.Bool
	stops   atomic.Int32
	mu      sync.Mutex
	store   map[string]string
}

func newFlakyMetadataServer(t *testing.T) *flakyMetadataServer {
	t.Helper()
	f := &flakyMetadataServer{store: map[string]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/machines") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode([]flyMachine{{ID: "m1"}})
		case strings.HasSuffix(r.URL.Path, "/stop") && r.Method == http.MethodPost:
			f.stops.Add(1)
			w.WriteHeader(http.StatusOK)
		case strings.Contains(r.URL.Path, "/metadata"):
			if f.failing.Load() {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			switch {
			case strings.Contains(r.URL.Path, "/metadata/") && r.Method == http.MethodPost:
				_, key := metadataPathParts(r.URL.Path)
				var body metadataValue
				_ = json.NewDecoder(r.Body).Decode(&body)
				f.store[key] = body.Value
			case strings.Contains(r.URL.Path, "/metadata/") && r.Method == http.MethodDelete:
				_, key := metadataPathParts(r.URL.Path)
				delete(f.store, key)
			default:
				_ = json.NewEncoder(w).Encode(f.store)
			}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	overrideBase(t, srv.URL)
	return f
}

func newFlakyLease(t *testing.T, f *flakyMetadataServer) *OllamaLease {
	t.Helper()
	lifecycle := NewOllamaLifecycle("app1", "test-token")
	lifecycle.client = http.DefaultClient
	return NewOllamaLease(lifecycle, "backend-a")
}

func TestOllamaLeaseDegradesOnlyAfterConsecutiveFailures(t *testing.T) {
	f := newFlakyMetadataServer(t)
	f.failing.Store(true)
	lease := newFlakyLease(t, f)
	ctx := context.Background()

	for i := 1; i < leaseDegradeAfterFailures; i++ {
		require.NoError(t, lease.Acquire(ctx), "Acquire must not surface a metadata failure")
		assert.False(t, lease.isDegraded(), "%d failure(s) must not degrade", i)
	}
	require.NoError(t, lease.Acquire(ctx))
	assert.True(t, lease.isDegraded())
}

func TestOllamaLeaseSuccessResetsFailureCounter(t *testing.T) {
	f := newFlakyMetadataServer(t)
	lease := newFlakyLease(t, f)
	ctx := context.Background()

	f.failing.Store(true)
	for i := 0; i < leaseDegradeAfterFailures-1; i++ {
		require.NoError(t, lease.Acquire(ctx))
	}
	f.failing.Store(false)
	require.NoError(t, lease.Acquire(ctx))
	f.failing.Store(true)
	for i := 0; i < leaseDegradeAfterFailures-1; i++ {
		require.NoError(t, lease.Acquire(ctx))
	}
	assert.False(t, lease.isDegraded(), "non-consecutive failures must not add up to degradation")
}

func TestOllamaLeaseDegradedThenRecovered(t *testing.T) {
	f := newFlakyMetadataServer(t)
	lease := newFlakyLease(t, f)
	ctx := context.Background()

	f.failing.Store(true)
	for i := 0; i < leaseDegradeAfterFailures; i++ {
		require.NoError(t, lease.Acquire(ctx))
	}
	require.True(t, lease.isDegraded())

	f.failing.Store(false)
	require.NoError(t, lease.Acquire(ctx), "Acquire must keep retrying while degraded")
	assert.False(t, lease.isDegraded(), "a success clears degradation")
	f.mu.Lock()
	_, wrote := f.store[lease.key]
	f.mu.Unlock()
	assert.True(t, wrote, "the recovered Acquire must actually write the lease")
}

func TestOllamaLeaseRenewLoopRetriesWhileDegraded(t *testing.T) {
	f := newFlakyMetadataServer(t)
	f.failing.Store(true)
	lease := newFlakyLease(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		lease.renewLoop(ctx, 10*time.Millisecond)
	}()

	require.Eventually(t, lease.isDegraded, 2*time.Second, 5*time.Millisecond)
	f.failing.Store(false)
	require.Eventually(t, func() bool { return !lease.isDegraded() }, 2*time.Second, 5*time.Millisecond,
		"a later tick must clear degradation without restarting the loop")
	cancel()
	<-done
}

func TestOllamaLeaseReleaseWhileDegraded(t *testing.T) {
	tests := []struct {
		name      string
		heldFirst bool
		wantStops int32
	}{
		{"never held a lease: plain StopAll", false, 1},
		{"previously held a lease: leave the shared box running", true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFlakyMetadataServer(t)
			lease := newFlakyLease(t, f)
			ctx := context.Background()
			if tt.heldFirst {
				require.NoError(t, lease.Acquire(ctx))
				require.True(t, lease.hasHeld())
			}
			f.failing.Store(true)
			for i := 0; i < leaseDegradeAfterFailures; i++ {
				require.NoError(t, lease.Acquire(ctx))
			}
			require.True(t, lease.isDegraded())

			require.NoError(t, lease.ReleaseAndMaybeStop(ctx))

			assert.Equal(t, tt.wantStops, f.stops.Load())
		})
	}
}
