package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeHourlyWriter struct {
	mu   sync.Mutex
	rows []HourlyRow
	err  error
}

func (f *fakeHourlyWriter) UpsertRequestHourly(_ context.Context, rows []HourlyRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.rows = append(f.rows, rows...)
	return nil
}

func TestP95(t *testing.T) {
	hundred := make([]int, 100)
	for i := range hundred {
		hundred[i] = i + 1
	}
	tests := []struct {
		name string
		in   []int
		want int
	}{
		{"empty", nil, 0},
		{"single", []int{7}, 7},
		{"twenty", []int{20, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19}, 19},
		{"hundred", hundred, 95},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, p95(tc.in))
		})
	}
}

func TestRequestMeter_AggregatesAndFlushesOnClose(t *testing.T) {
	w := &fakeHourlyWriter{}
	m := NewRequestMeter(w, time.Hour)
	fixed := time.Date(2026, 11, 26, 10, 42, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }
	m.Start()

	m.Observe("", 200, time.Millisecond) // ignored: no tenant
	m.Observe("t1", 200, 10*time.Millisecond)
	m.Observe("t1", 500, 30*time.Millisecond)
	m.Observe("t2", 404, 5*time.Millisecond)

	require.NoError(t, m.Close(context.Background()))
	require.Len(t, w.rows, 2)
	got := map[string]HourlyRow{}
	for _, r := range w.rows {
		got[r.TenantID] = r
	}
	assert.Equal(t, HourlyRow{TenantID: "t1", Hour: fixed.Truncate(time.Hour), Requests: 2, Errors5xx: 1, HandlerMs: 40, P95Ms: 30}, got["t1"])
	assert.Equal(t, int64(0), got["t2"].Errors5xx)
	assert.Equal(t, int64(1), got["t2"].Requests)
}

func TestRequestMeter_ReservoirBounded(t *testing.T) {
	m := NewRequestMeter(&fakeHourlyWriter{}, time.Hour)
	for i := 0; i < meterReservoirSize*3; i++ {
		m.Observe("t1", 200, time.Millisecond)
	}
	for _, b := range m.buckets {
		assert.Len(t, b.samples, meterReservoirSize)
		assert.Equal(t, int64(meterReservoirSize*3), b.requests)
	}
}

func TestRequestMeter_FailedWriteDropsAndCloseIdempotent(t *testing.T) {
	w := &fakeHourlyWriter{err: errors.New("down")}
	m := NewRequestMeter(w, time.Hour)
	m.Observe("t1", 200, time.Millisecond)
	assert.Error(t, m.Close(context.Background()))
	assert.NoError(t, m.Close(context.Background()))
	assert.Empty(t, m.buckets)
}
