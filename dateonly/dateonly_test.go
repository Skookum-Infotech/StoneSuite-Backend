package dateonly

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMarshalJSON(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	tests := []struct {
		name string
		in   Date
		want string
	}{
		{"utc midnight", New(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)), `"2026-01-02"`},
		{"late evening keeps its own day", New(time.Date(2026, 1, 2, 23, 30, 0, 0, chicago)), `"2026-01-02"`},
		{"zero", Date{}, `"0001-01-01"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(tc.in)
			require.NoError(t, err)
			assert.Equal(t, tc.want, string(b))
		})
	}
}

func TestUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"date only", `"2026-03-04"`, "2026-03-04", false},
		{"rfc3339 from older clients", `"2026-03-04T00:00:00Z"`, "2026-03-04", false},
		{"garbage", `"March 4"`, "", true},
		{"not a string", `20260304`, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d Date
			err := json.Unmarshal([]byte(tc.in), &d)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, d.String())
		})
	}
}

func TestScan(t *testing.T) {
	tests := []struct {
		name    string
		src     any
		want    string
		wantErr bool
	}{
		{"time", time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC), "2026-05-06", false},
		{"string", "2026-05-06", "2026-05-06", false},
		{"nil is zero", nil, "0001-01-01", false},
		{"unsupported", 42, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d Date
			err := d.Scan(tc.src)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, d.String())
		})
	}
}

func TestScanDate(t *testing.T) {
	tests := []struct {
		name    string
		in      pgtype.Date
		want    string
		wantErr bool
	}{
		{"valid", pgtype.Date{Time: time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC), Valid: true}, "2026-07-08", false},
		{"null is zero", pgtype.Date{}, "0001-01-01", false},
		{"infinity", pgtype.Date{InfinityModifier: pgtype.Infinity, Valid: true}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d Date
			err := d.ScanDate(tc.in)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, d.String())
		})
	}
}
