package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/storage"
)

type fakeSnapshots struct {
	mu   sync.Mutex
	snap map[string]*StorageSnapshot
	err  error
}

func (f *fakeSnapshots) StorageSnapshot(_ context.Context, tenantID string) (*StorageSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap[tenantID], f.err
}

func (f *fakeSnapshots) UpsertStorageSnapshot(_ context.Context, tenantID string, snap StorageSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snap[tenantID] = &snap
	return nil
}

type fakeMeasurer struct {
	mu      sync.Mutex
	usage   storage.BucketUsage
	err     error
	buckets []string
}

func (f *fakeMeasurer) WithBucketUsage(_ context.Context, bucket string) (storage.BucketUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.buckets = append(f.buckets, bucket)
	return f.usage, f.err
}

func TestStorageRefresher_Snapshot(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	fresh := &StorageSnapshot{UsedBytes: 10, ObjectCount: 1, MeasuredAt: now.Add(-time.Hour)}
	stale := &StorageSnapshot{UsedBytes: 10, ObjectCount: 1, MeasuredAt: now.Add(-StorageStaleAfter - time.Minute)}
	measured := storage.BucketUsage{Bytes: 500, Objects: 3}

	tests := []struct {
		name         string
		existing     *StorageSnapshot
		bucket       string
		measureErr   error
		wantSnap     *StorageSnapshot
		wantMeasured int
		wantStored   *StorageSnapshot
	}{
		{"never measured: measures now", nil, "ss-acme", nil,
			&StorageSnapshot{UsedBytes: 500, ObjectCount: 3, MeasuredAt: now}, 1,
			&StorageSnapshot{UsedBytes: 500, ObjectCount: 3, MeasuredAt: now}},
		{"never measured, listing fails: nil, retried in background", nil, "ss-acme", errors.New("403"), nil, 2, nil},
		{"fresh: no listing", fresh, "ss-acme", nil, fresh, 0, fresh},
		{"stale: returns old, refreshes in background", stale, "ss-acme", nil, stale, 1,
			&StorageSnapshot{UsedBytes: 500, ObjectCount: 3, MeasuredAt: now}},
		{"no bucket provisioned", nil, "", nil, nil, 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeSnapshots{snap: map[string]*StorageSnapshot{}}
			if tc.existing != nil {
				store.snap["t1"] = tc.existing
			}
			m := &fakeMeasurer{usage: measured, err: tc.measureErr}
			r := NewStorageRefresher(store, m)
			r.now = func() time.Time { return now }

			got, err := r.Snapshot(context.Background(), "t1", tc.bucket)
			require.NoError(t, err)
			assert.Equal(t, tc.wantSnap, got)
			r.Wait()
			assert.Len(t, m.buckets, tc.wantMeasured)
			for _, b := range m.buckets {
				assert.Equal(t, tc.bucket, b, "only the tenant's own bucket is measured")
			}
			assert.Equal(t, tc.wantStored, store.snap["t1"])
		})
	}
}

func TestStorageRefresher_StoreError(t *testing.T) {
	r := NewStorageRefresher(&fakeSnapshots{err: errors.New("cp down")}, &fakeMeasurer{})
	_, err := r.Snapshot(context.Background(), "t1", "ss-acme")
	assert.Error(t, err)
}

func TestStorageRefresher_OneRefreshInFlightPerTenant(t *testing.T) {
	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	stale := &StorageSnapshot{MeasuredAt: now.Add(-2 * StorageStaleAfter)}
	store := &fakeSnapshots{snap: map[string]*StorageSnapshot{"t1": stale}}
	r := NewStorageRefresher(store, &fakeMeasurer{})
	r.now = func() time.Time { return now }
	r.mu.Lock()
	r.inflight["t1"] = true // a refresh is already running
	r.mu.Unlock()
	_, err := r.Snapshot(context.Background(), "t1", "ss-acme")
	require.NoError(t, err)
	r.Wait()
	assert.Same(t, stale, store.snap["t1"], "no second refresh started")
}
