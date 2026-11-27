package usage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"stonesuite-backend/storage"
)

// Storage refresh tuning. The backend scales to zero, so instead of a
// periodic sweep a tenant's bucket is re-measured when its Subscription page
// is viewed and the last measurement is older than StorageStaleAfter.
const (
	StorageStaleAfter = 6 * time.Hour
	// storageMeasureTimeout bounds one bucket listing (a page is 1000 keys).
	storageMeasureTimeout = 60 * time.Second
	// storageFirstMeasureTimeout bounds the synchronous first measurement a
	// never-measured tenant waits for; past it the page shows "not measured".
	storageFirstMeasureTimeout = 10 * time.Second
)

// bucketMeasurer lists one bucket's objects (*storage.Client in production).
type bucketMeasurer interface {
	WithBucketUsage(ctx context.Context, bucket string) (storage.BucketUsage, error)
}

// snapshotStore persists storage measurements (*Store in production).
type snapshotStore interface {
	StorageSnapshot(ctx context.Context, tenantID string) (*StorageSnapshot, error)
	UpsertStorageSnapshot(ctx context.Context, tenantID string, snap StorageSnapshot) error
}

// R2Measurer adapts the shared R2 client to measure one named bucket.
type R2Measurer struct{ Client *storage.Client }

// WithBucketUsage measures bucket — always the tenant's own tenants.r2_bucket.
func (m R2Measurer) WithBucketUsage(ctx context.Context, bucket string) (storage.BucketUsage, error) {
	return m.Client.WithBucket(bucket).Usage(ctx)
}

// StorageRefresher returns a tenant's storage snapshot, measuring the
// tenant's own bucket when it is missing (synchronously, bounded) or stale
// (in the background, at most one in flight per tenant).
type StorageRefresher struct {
	store    snapshotStore
	measurer bucketMeasurer
	now      func() time.Time

	mu       sync.Mutex
	inflight map[string]bool
	wg       sync.WaitGroup
}

// NewStorageRefresher builds a refresher over the usage store and R2 client.
func NewStorageRefresher(store snapshotStore, measurer bucketMeasurer) *StorageRefresher {
	return &StorageRefresher{store: store, measurer: measurer, now: time.Now, inflight: map[string]bool{}}
}

// Snapshot returns tenantID's latest measurement of bucket, or nil when the
// tenant has no bucket or has not been measured yet.
func (s *StorageRefresher) Snapshot(ctx context.Context, tenantID, bucket string) (*StorageSnapshot, error) {
	snap, err := s.store.StorageSnapshot(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if bucket == "" {
		return snap, nil
	}
	if snap == nil {
		first, cancel := context.WithTimeout(ctx, storageFirstMeasureTimeout)
		defer cancel()
		measured, err := s.measure(first, tenantID, bucket)
		if err != nil {
			slog.WarnContext(ctx, "usage: first storage measurement failed", "tenant_id", tenantID, "err", err)
			s.refreshAsync(tenantID, bucket)
			return nil, nil
		}
		return measured, nil
	}
	if s.now().Sub(snap.MeasuredAt) > StorageStaleAfter {
		s.refreshAsync(tenantID, bucket)
	}
	return snap, nil
}

// Wait blocks until every background refresh has finished (for shutdown/tests).
func (s *StorageRefresher) Wait() { s.wg.Wait() }

// refreshAsync re-measures in the background unless one is already running
// for the tenant. The goroutine exits when the bounded measurement does.
func (s *StorageRefresher) refreshAsync(tenantID, bucket string) {
	s.mu.Lock()
	if s.inflight[tenantID] {
		s.mu.Unlock()
		return
	}
	s.inflight[tenantID] = true
	s.mu.Unlock()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.inflight, tenantID)
			s.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), storageMeasureTimeout)
		defer cancel()
		if _, err := s.measure(ctx, tenantID, bucket); err != nil {
			slog.Error("usage: storage refresh failed", "tenant_id", tenantID, "err", err)
		}
	}()
}

// Measure lists the tenant's bucket and persists a fresh snapshot of it.
func (s *StorageRefresher) Measure(ctx context.Context, tenantID, bucket string) (*StorageSnapshot, error) {
	return s.measure(ctx, tenantID, bucket)
}

func (s *StorageRefresher) measure(ctx context.Context, tenantID, bucket string) (*StorageSnapshot, error) {
	if s.measurer == nil {
		return nil, errors.New("r2 not configured")
	}
	u, err := s.measurer.WithBucketUsage(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("measure bucket for tenant %s: %w", tenantID, err)
	}
	snap := StorageSnapshot{UsedBytes: u.Bytes, ObjectCount: u.Objects, MeasuredAt: s.now().UTC()}
	if err := s.store.UpsertStorageSnapshot(ctx, tenantID, snap); err != nil {
		return nil, err
	}
	return &snap, nil
}
