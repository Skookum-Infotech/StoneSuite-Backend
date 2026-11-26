package usage

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func idxDay(d int) time.Time { return time.Date(2026, time.November, d, 0, 0, 0, 0, time.UTC) }

func TestValueIndex_Aggregates(t *testing.T) {
	const m = "m"
	x := NewValueIndex(
		[]DailyValue{
			{TenantID: "a", Day: idxDay(1), Key: m, Value: 2},
			{TenantID: "a", Day: idxDay(3), Key: m, Value: 8},
			{TenantID: "a", Day: idxDay(4), Key: m, Value: 5},
			{TenantID: "b", Day: idxDay(2), Key: m, Value: 100},
		},
		// A later set overwrites an earlier one for the same tenant/metric/day.
		[]DailyValue{{TenantID: "a", Day: idxDay(4).Add(9 * time.Hour), Key: m, Value: 6}},
	)

	tests := []struct {
		name     string
		fn       func(string, string, time.Time, time.Time) (float64, bool)
		tenant   string
		from, to time.Time
		want     float64
		wantOK   bool
	}{
		{"sum skips gaps", x.Sum, "a", idxDay(1), idxDay(4), 16, true},
		{"sum single day", x.Sum, "a", idxDay(3), idxDay(3), 8, true},
		{"sum no data is unmeasured", x.Sum, "a", idxDay(10), idxDay(12), 0, false},
		{"sum other tenant isolated", x.Sum, "b", idxDay(1), idxDay(4), 100, true},
		{"sum unknown tenant", x.Sum, "zzz", idxDay(1), idxDay(4), 0, false},
		{"sum inverted range", x.Sum, "a", idxDay(4), idxDay(1), 0, false},
		{"max", x.Max, "a", idxDay(1), idxDay(4), 8, true},
		{"max no data", x.Max, "a", idxDay(10), idxDay(11), 0, false},
		{"last is latest day with data", x.Last, "a", idxDay(1), idxDay(10), 6, true},
		{"last ignores days outside range", x.Last, "a", idxDay(1), idxDay(2), 2, true},
		{"last no data", x.Last, "a", idxDay(5), idxDay(9), 0, false},
		{"mean over days with data only", x.Mean, "a", idxDay(1), idxDay(4), 16.0 / 3, true},
		{"mean no data", x.Mean, "a", idxDay(5), idxDay(9), 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := tc.fn(tc.tenant, m, tc.from, tc.to)
			assert.Equal(t, tc.wantOK, ok)
			assert.InDelta(t, tc.want, got, 1e-9)
		})
	}

	t.Run("max handles negative values", func(t *testing.T) {
		neg := NewValueIndex([]DailyValue{
			{TenantID: "a", Day: idxDay(1), Key: m, Value: -5},
			{TenantID: "a", Day: idxDay(2), Key: m, Value: -2},
		})
		got, ok := neg.Max("a", m, idxDay(1), idxDay(2))
		assert.True(t, ok)
		assert.Equal(t, -2.0, got)
	})

	t.Run("at distinguishes a zero value from missing", func(t *testing.T) {
		z := NewValueIndex([]DailyValue{{TenantID: "a", Day: idxDay(1), Key: m, Value: 0}})
		v, ok := z.At("a", m, idxDay(1))
		assert.True(t, ok)
		assert.Zero(t, v)
		_, ok = z.At("a", m, idxDay(2))
		assert.False(t, ok)
	})
}

func TestComputeTrend(t *testing.T) {
	w := NewWindow(idxDay(5), 5)
	x := NewValueIndex([]DailyValue{
		{TenantID: "a", Day: idxDay(1), Key: MetricComputeRequestHours, Value: 1},
		{TenantID: "a", Day: idxDay(1), Key: MetricComputeJobHours, Value: 0.5},
		{TenantID: "a", Day: idxDay(3), Key: MetricComputeJobHours, Value: 2},
		{TenantID: "a", Day: idxDay(5), Key: MetricComputeRequestHours, Value: 4},
		{TenantID: "b", Day: idxDay(2), Key: MetricComputeRequestHours, Value: 99},
	})
	tests := []struct {
		name   string
		tenant string
		want   []float64
	}{
		{"sums request+job and zero-fills gaps", "a", []float64{1.5, 0, 2, 0, 4}},
		{"tenant without data is all zeros, full length", "none", []float64{0, 0, 0, 0, 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ComputeTrend(x, tc.tenant, w))
		})
	}
}

func TestWindowDates(t *testing.T) {
	w := NewWindow(idxDay(7), 3)
	assert.Equal(t, []time.Time{idxDay(5), idxDay(6), idxDay(7)}, w.Dates())
	assert.Equal(t, idxDay(4), w.Prev().End)
	assert.Equal(t, idxDay(2), w.Prev().Start())
}

func TestErrorRatePct(t *testing.T) {
	tests := []struct {
		name       string
		errs, reqs nsum
		want       *float64
	}{
		{"no requests measured", nsum{}, nsum{}, nil},
		{"zero requests", nsum{v: 0, ok: true}, nsum{v: 0, ok: true}, nil},
		{"errors unmeasured counts as zero percent", nsum{}, nsum{v: 50, ok: true}, ptrF(0)},
		{"two percent", nsum{v: 1, ok: true}, nsum{v: 50, ok: true}, ptrF(2)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := errorRatePct(tc.errs, tc.reqs)
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			if assert.NotNil(t, got) {
				assert.InDelta(t, *tc.want, *got, 1e-9)
			}
		})
	}
}

func TestNsum(t *testing.T) {
	var n nsum
	assert.Nil(t, n.ptr(), "unmeasured until a value is added")
	n.add(5, false)
	assert.Nil(t, n.ptr(), "an unmeasured add does not flip it")
	n.add(0, true)
	if assert.NotNil(t, n.ptr()) {
		assert.Zero(t, *n.ptr(), "a measured zero is not nil")
	}
	n.add(3, true)
	assert.Equal(t, 3.0, *n.ptr())
}

func TestStatsHelpers(t *testing.T) {
	t.Run("lastValue", func(t *testing.T) {
		assert.Zero(t, lastValue(nil))
		assert.Equal(t, 3.0, lastValue([]float64{1, 3}))
	})
	t.Run("sumLast", func(t *testing.T) {
		tests := []struct {
			name string
			in   []float64
			n    int
			want float64
		}{
			{"empty", nil, 3, 0},
			{"n larger than len", []float64{1, 2}, 5, 3},
			{"tail only", []float64{1, 2, 3, 4}, 2, 7},
			{"n zero", []float64{1, 2}, 0, 0},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, sumLast(tc.in, tc.n)) })
		}
	})
	t.Run("meanStd", func(t *testing.T) {
		m, s := meanStd(nil)
		assert.Zero(t, m)
		assert.Zero(t, s)
		m, s = meanStd([]float64{2, 4, 4, 4, 5, 5, 7, 9})
		assert.InDelta(t, 5, m, 1e-9)
		assert.InDelta(t, 2, s, 1e-9)
	})
	t.Run("median does not mutate input", func(t *testing.T) {
		in := []float64{3, 1, 2}
		assert.Equal(t, 2.0, median(in))
		assert.Equal(t, []float64{3, 1, 2}, in)
		assert.Equal(t, 2.5, median([]float64{4, 1, 2, 3}))
		assert.Zero(t, median(nil))
	})
}

func TestFormatHelpers(t *testing.T) {
	assert.Equal(t, "1.5 h", formatHours(1.5))
	assert.Equal(t, "0.0 h", formatHours(0))
	assert.Equal(t, "12.3%", formatPercentOne(12.34))
	assert.Equal(t, "1 request", "1 "+pluralize(1, "request", "requests"))
	assert.Equal(t, "0 requests", "0 "+pluralize(0, "request", "requests"))
	assert.Equal(t, "2 requests", "2 "+pluralize(2, "request", "requests"))

	tests := []struct {
		name string
		card string
		v    float64
		want string
	}{
		{"db is GB", CardDB, 2 * bytesPerGB, "2.0 GB"},
		{"r2 is GB", CardR2, 150 * bytesPerGB, "150 GB"},
		{"compute is hours", CardCompute, 3.26, "3.3 h"},
		{"tokens are counts", CardAITokens, 1234567.4, "1,234,567"},
		{"requests round half up", CardRequests, math.Round(999.5), "1,000"},
		{"users", CardUsers, 7, "7"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, formatCardValue(tc.card, tc.v)) })
	}
}

func ptrF(v float64) *float64 { return &v }
