package main

import (
	"math"
	"sort"
	"time"
)

// percentileMillis returns the p-th percentile (nearest-rank method, e.g.
// p=50 for p50, p=90 for p90) of durs in whole milliseconds. Returns 0 for
// an empty input. durs need not be pre-sorted.
func percentileMillis(durs []time.Duration, p float64) int64 {
	if len(durs) == 0 {
		return 0
	}
	sorted := make([]time.Duration, len(durs))
	copy(sorted, durs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1].Milliseconds()
}
