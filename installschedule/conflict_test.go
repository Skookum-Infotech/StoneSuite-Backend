package installschedule

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOverlaps(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	at := func(d, h int) time.Time { return time.Date(2026, 11, d, h, 0, 0, 0, loc).UTC() }
	iv := func(s, e time.Time) Interval { return Interval{Start: s, End: e} }
	allDay := Interval{Start: at(10, 0), End: at(11, 0), AllDay: true}

	tests := []struct {
		name string
		a, b Interval
		want bool
	}{
		{"back to back", iv(at(10, 8), at(10, 12)), iv(at(10, 12), at(10, 16)), false},
		{"partial overlap", iv(at(10, 8), at(10, 12)), iv(at(10, 11), at(10, 13)), true},
		{"all day vs morning slot", allDay, iv(at(10, 8), at(10, 9)), true},
		{"identical", iv(at(10, 8), at(10, 12)), iv(at(10, 8), at(10, 12)), true},
		{"disjoint", iv(at(10, 8), at(10, 9)), iv(at(11, 8), at(11, 9)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Overlaps(tt.a, tt.b))
			assert.Equal(t, tt.want, Overlaps(tt.b, tt.a))
		})
	}
}

func TestSortedUniqueIDs(t *testing.T) {
	tests := []struct {
		name string
		in   []int
		want []int
	}{
		{"empty", nil, []int{}},
		{"sorted dedupe", []int{5, 3, 5, 1, 3}, []int{1, 3, 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sortedUniqueIDs(tt.in))
		})
	}
}
