package usage

import (
	"math"
	"sort"
)

// FleetP95 returns the nearest-rank 95th percentile of values, or 0 for empty input.
func FleetP95(values []float64) float64 {
	n := len(values)
	if n == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	rank := (fleetPercentile*n + percentDivisor - 1) / percentDivisor
	if rank < 1 {
		rank = 1
	}
	return sorted[rank-1]
}

// cardSeries returns the daily series behind a card; compute sums request and job hours per day.
func cardSeries(in RuleInput, card string) []float64 {
	switch card {
	case CardCompute:
		req, job := in.Series[MetricComputeRequestHours], in.Series[MetricComputeJobHours]
		n := max(len(req), len(job))
		sum := make([]float64, n)
		for i := 0; i < n; i++ {
			if j := i - (n - len(req)); j >= 0 {
				sum[i] += req[j]
			}
			if j := i - (n - len(job)); j >= 0 {
				sum[i] += job[j]
			}
		}
		return sum
	case CardDB:
		return in.Series[MetricDBBytes]
	case CardR2:
		return in.Series[MetricR2Bytes]
	case CardAITokens:
		return in.Series[MetricAITokens]
	case CardRequests:
		return in.Series[MetricRequests]
	case CardUsers:
		return in.Series[MetricActiveUsers]
	}
	return nil
}

// lastValue returns the final element of s, or 0 when empty.
func lastValue(s []float64) float64 {
	if len(s) == 0 {
		return 0
	}
	return s[len(s)-1]
}

// sumLast sums the last n elements of s.
func sumLast(s []float64, n int) float64 {
	total := 0.0
	for i := max(0, len(s)-n); i < len(s); i++ {
		total += s[i]
	}
	return total
}

// meanStd returns the mean and population standard deviation of v.
func meanStd(v []float64) (float64, float64) {
	if len(v) == 0 {
		return 0, 0
	}
	mean := sumLast(v, len(v)) / float64(len(v))
	var sq float64
	for _, x := range v {
		sq += (x - mean) * (x - mean)
	}
	return mean, math.Sqrt(sq / float64(len(v)))
}

// median returns the median of v without modifying it.
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}
