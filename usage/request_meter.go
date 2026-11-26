package usage

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// DefaultMeterFlushInterval is how often the request meter writes its buckets.
const DefaultMeterFlushInterval = 60 * time.Second

// meterReservoirSize bounds the latency samples kept per tenant-hour for p95.
const meterReservoirSize = 512

// meterWriteTimeout bounds one flush write so a slow control plane cannot wedge the meter.
const meterWriteTimeout = 10 * time.Second

// p95Percentile is the percentile reported per tenant-hour.
const p95Percentile = 95

// percentScale is the denominator for percentile rank arithmetic.
const percentScale = 100

// hourlyWriter persists hourly request rows (*FleetStore in production).
type hourlyWriter interface {
	UpsertRequestHourly(ctx context.Context, rows []HourlyRow) error
}

type meterKey struct {
	tenantID string
	hour     time.Time
}

type meterBucket struct {
	requests  int64
	errors5xx int64
	handlerMs int64
	seen      int64
	samples   []int
}

// RequestMeter aggregates per-tenant request counts and latency into hourly
// buckets and flushes them asynchronously. Observe is O(1) and never fails or
// blocks on I/O, so metering can never affect a request.
type RequestMeter struct {
	writer     hourlyWriter
	flushEvery time.Duration
	now        func() time.Time

	mu      sync.Mutex
	buckets map[meterKey]*meterBucket
	rng     uint64

	stop    chan struct{}
	done    chan struct{}
	started bool
	closed  bool
	closeMu sync.Mutex
	flushMu sync.Mutex
}

// NewRequestMeter builds a meter that flushes to writer every flushEvery.
func NewRequestMeter(writer hourlyWriter, flushEvery time.Duration) *RequestMeter {
	return &RequestMeter{
		writer:     writer,
		flushEvery: flushEvery,
		now:        time.Now,
		buckets:    make(map[meterKey]*meterBucket),
		rng:        1,
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
}

// Start runs the flush goroutine; it exits when Close is called.
func (m *RequestMeter) Start() {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()
	if m.started || m.closed {
		return
	}
	m.started = true
	go func() {
		defer close(m.done)
		t := time.NewTicker(m.flushEvery)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				m.flush(context.Background())
			case <-m.stop:
				return
			}
		}
	}()
}

// Observe records one finished request for tenantID (status >= 500 counts as an error).
func (m *RequestMeter) Observe(tenantID string, status int, elapsed time.Duration) {
	if tenantID == "" {
		return
	}
	ms := int(elapsed.Milliseconds())
	key := meterKey{tenantID: tenantID, hour: m.now().UTC().Truncate(time.Hour)}

	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[key]
	if b == nil {
		b = &meterBucket{}
		m.buckets[key] = b
	}
	b.requests++
	if status >= 500 {
		b.errors5xx++
	}
	b.handlerMs += int64(ms)
	b.seen++
	if len(b.samples) < meterReservoirSize {
		b.samples = append(b.samples, ms)
		return
	}
	// Reservoir sampling (algorithm R) with a cheap xorshift generator.
	m.rng ^= m.rng << 13
	m.rng ^= m.rng >> 7
	m.rng ^= m.rng << 17
	if j := int(m.rng % uint64(b.seen)); j < meterReservoirSize {
		b.samples[j] = ms
	}
}

// Close stops the flush goroutine and performs a final flush bounded by ctx.
func (m *RequestMeter) Close(ctx context.Context) error {
	m.closeMu.Lock()
	if m.closed {
		m.closeMu.Unlock()
		return nil
	}
	m.closed = true
	started := m.started
	close(m.stop)
	m.closeMu.Unlock()

	if started {
		select {
		case <-m.done:
		case <-ctx.Done():
			return fmt.Errorf("wait for request meter: %w", ctx.Err())
		}
	}
	if err := m.flushWith(ctx); err != nil {
		return fmt.Errorf("final request meter flush: %w", err)
	}
	return nil
}

// flush writes the current buckets using a bounded background context; failures are logged and dropped.
func (m *RequestMeter) flush(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, meterWriteTimeout)
	defer cancel()
	if err := m.flushWith(ctx); err != nil {
		slog.Error("usage: failed to flush request meter", "err", err)
	}
}

// flushWith swaps the buckets out under the lock and writes them outside it.
func (m *RequestMeter) flushWith(ctx context.Context) error {
	m.flushMu.Lock()
	defer m.flushMu.Unlock()

	m.mu.Lock()
	buckets := m.buckets
	m.buckets = make(map[meterKey]*meterBucket)
	m.mu.Unlock()
	if len(buckets) == 0 {
		return nil
	}
	rows := make([]HourlyRow, 0, len(buckets))
	for k, b := range buckets {
		rows = append(rows, HourlyRow{
			TenantID:  k.tenantID,
			Hour:      k.hour,
			Requests:  b.requests,
			Errors5xx: b.errors5xx,
			HandlerMs: b.handlerMs,
			P95Ms:     p95(b.samples),
		})
	}
	if err := m.writer.UpsertRequestHourly(ctx, rows); err != nil {
		return fmt.Errorf("write request hourly: %w", err)
	}
	return nil
}

// p95 returns the nearest-rank 95th percentile of samples (0 when empty).
func p95(samples []int) int {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]int(nil), samples...)
	sort.Ints(sorted)
	rank := (p95Percentile*len(sorted) + percentScale - 1) / percentScale // ceil(0.95*n)
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}
