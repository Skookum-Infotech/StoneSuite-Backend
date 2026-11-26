package usage

import "time"

type seriesKey struct{ tenant, metric string }

// ValueIndex is an in-memory index of daily metric values by tenant, metric and day.
type ValueIndex struct {
	vals map[seriesKey]map[string]float64
}

// NewValueIndex indexes the given daily value sets (later sets overwrite earlier ones).
func NewValueIndex(sets ...[]DailyValue) *ValueIndex {
	x := &ValueIndex{vals: map[seriesKey]map[string]float64{}}
	for _, set := range sets {
		for _, v := range set {
			k := seriesKey{v.TenantID, v.Key}
			if x.vals[k] == nil {
				x.vals[k] = map[string]float64{}
			}
			x.vals[k][v.Day.UTC().Format(dateLayout)] = v.Value
		}
	}
	return x
}

// At returns the value on one day.
func (x *ValueIndex) At(tenant, metric string, day time.Time) (float64, bool) {
	v, ok := x.vals[seriesKey{tenant, metric}][day.UTC().Format(dateLayout)]
	return v, ok
}

// Sum adds the values over [from, to]; ok is false when no day has data.
func (x *ValueIndex) Sum(tenant, metric string, from, to time.Time) (float64, bool) {
	var total float64
	var found bool
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if v, ok := x.At(tenant, metric, d); ok {
			total += v
			found = true
		}
	}
	return total, found
}

// Max returns the largest value over [from, to]; ok is false when no day has data.
func (x *ValueIndex) Max(tenant, metric string, from, to time.Time) (float64, bool) {
	var best float64
	var found bool
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if v, ok := x.At(tenant, metric, d); ok && (!found || v > best) {
			best, found = v, true
		}
	}
	return best, found
}

// Last returns the value on the latest day in [from, to] that has data.
func (x *ValueIndex) Last(tenant, metric string, from, to time.Time) (float64, bool) {
	for d := to; !d.Before(from); d = d.AddDate(0, 0, -1) {
		if v, ok := x.At(tenant, metric, d); ok {
			return v, true
		}
	}
	return 0, false
}

// Mean averages the values over [from, to] across days that have data.
func (x *ValueIndex) Mean(tenant, metric string, from, to time.Time) (float64, bool) {
	var total float64
	var n int
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		if v, ok := x.At(tenant, metric, d); ok {
			total += v
			n++
		}
	}
	if n == 0 {
		return 0, false
	}
	return total / float64(n), true
}
