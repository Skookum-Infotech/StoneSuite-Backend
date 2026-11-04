package installschedule

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	return loc
}

func TestParseLocal(t *testing.T) {
	loc := chicago(t)
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr error
		anyErr  bool
	}{
		{name: "standard time", in: "2026-11-10T08:00", want: "2026-11-10T14:00:00Z"},
		{name: "dst gap", in: "2026-03-08T02:30", wantErr: ErrNonexistentLocalTime},
		{name: "ambiguous picks first", in: "2026-11-01T01:30", want: "2026-11-01T06:30:00Z"},
		{name: "wrong layout", in: "2026-11-10T08:00:00", anyErr: true},
		{name: "empty", in: "", anyErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseLocal(tc.in, loc)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.True(t, errors.Is(err, tc.wantErr))
				return
			}
			if tc.anyErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.UTC().Format(time.RFC3339))
		})
	}
}

func TestResolveInterval(t *testing.T) {
	loc := chicago(t)
	tests := []struct {
		name      string
		start     string
		end       string
		allDay    bool
		wantDur   time.Duration
		wantField string
	}{
		{name: "all-day spring forward", start: "2026-03-08", end: "2026-03-08", allDay: true, wantDur: 23 * time.Hour},
		{name: "all-day fall back two days", start: "2026-11-01", end: "2026-11-02", allDay: true, wantDur: 49 * time.Hour},
		{name: "timed ok", start: "2026-11-10T08:00", end: "2026-11-10T12:00", wantDur: 4 * time.Hour},
		{name: "zero length", start: "2026-11-10T08:00", end: "2026-11-10T08:00", wantField: "endLocal"},
		{name: "too long", start: "2026-11-10T08:00", end: "2026-11-24T08:15", wantField: "endLocal"},
		{name: "bad minute step", start: "2026-11-10T08:10", end: "2026-11-10T12:00", wantField: "startLocal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveInterval(tc.start, tc.end, tc.allDay, loc)
			if tc.wantField != "" {
				var ve *ValidationError
				require.True(t, errors.As(err, &ve))
				assert.Equal(t, tc.wantField, ve.Field)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantDur, got.End.Sub(got.Start))
			assert.Equal(t, "America/Chicago", got.TZ)
			assert.Equal(t, tc.allDay, got.AllDay)
			if tc.allDay {
				assert.Equal(t, tc.start, got.LocalStartDate.String())
				assert.Equal(t, tc.end, got.LocalEndDate.String())
			}
		})
	}
}

func TestResolveIntervalMinuteMessage(t *testing.T) {
	_, err := ResolveInterval("2026-11-10T08:10", "2026-11-10T12:00", false, chicago(t))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "15-minute steps")
}
