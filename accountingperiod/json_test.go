package accountingperiod

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func toMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(b, &m))
	return m
}

func TestWireShape(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	ts := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	rfc := ts.Format(time.RFC3339)

	period := Period{ID: "p1", Number: 1, Start: start, End: end, Status: StatusOpen,
		APLockStatus: "open", CreatedAt: ts, UpdatedAt: ts, ClosedAt: &ts}
	quarter := Quarter{ID: "q1", Number: 1, Start: start, End: end, CreatedAt: ts, UpdatedAt: ts}
	fy := FiscalYear{ID: "fy1", Start: start, End: end, Status: StatusOpen,
		Periods: []Period{period}, Quarters: []Quarter{quarter}, CreatedAt: ts, UpdatedAt: ts}

	tests := []struct {
		name string
		v    any
		want map[string]any
	}{
		{"period", period, map[string]any{"start": "2026-01-01", "end": "2026-01-31", "id": "p1", "status": "open",
			"periodNumber": float64(1), "apLockStatus": "open", "createdAt": rfc, "closedAt": rfc}},
		{"quarter", quarter, map[string]any{"start": "2026-01-01", "end": "2026-01-31", "id": "q1",
			"quarterNumber": float64(1), "createdAt": rfc}},
		{"fiscalYear", fy, map[string]any{"start": "2026-01-01", "end": "2026-01-31", "id": "fy1",
			"status": "open", "createdAt": rfc}},
		{"calendar", Calendar{Configured: true, BasePeriodStart: &start, BooksClosedThrough: &end, ConfiguredAt: &ts},
			map[string]any{"configured": true, "basePeriodStart": "2026-01-01",
				"booksClosedThrough": "2026-01-31", "configuredAt": rfc}},
		{"statusChange", StatusChangeResult{Periods: []Period{period}, BooksClosedThrough: &end},
			map[string]any{"booksClosedThrough": "2026-01-31"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := toMap(t, tt.v)
			for k, v := range tt.want {
				assert.Equal(t, v, m[k], "key %s", k)
			}
			// Pointer marshaling matches value marshaling.
			assert.Equal(t, m, toMap(t, &tt.v))
		})
	}

	t.Run("pointerReceiverTypes", func(t *testing.T) {
		assert.Equal(t, toMap(t, period), toMap(t, &period))
		assert.Equal(t, toMap(t, fy), toMap(t, &fy))
	})

	t.Run("nestedFiscalYear", func(t *testing.T) {
		m := toMap(t, fy)
		p := m["periods"].([]any)[0].(map[string]any)
		q := m["quarters"].([]any)[0].(map[string]any)
		assert.Equal(t, "2026-01-01", p["start"])
		assert.Equal(t, "2026-01-31", p["end"])
		assert.Equal(t, "2026-01-01", q["start"])
	})

	t.Run("nestedStatusChange", func(t *testing.T) {
		m := toMap(t, StatusChangeResult{Periods: []Period{period}})
		p := m["periods"].([]any)[0].(map[string]any)
		assert.Equal(t, "2026-01-01", p["start"])
	})

	t.Run("omitemptyPreserved", func(t *testing.T) {
		m := toMap(t, Period{Start: start, End: end})
		assert.NotContains(t, m, "quarterId")
		assert.NotContains(t, m, "quarterName")
		assert.NotContains(t, m, "closedAt")
		assert.Contains(t, m, "apLockStatus")
		m = toMap(t, Period{Start: start, End: end, QuarterID: "q1"})
		assert.Equal(t, "q1", m["quarterId"])
		m = toMap(t, FiscalYear{Start: start, End: end})
		assert.NotContains(t, m, "periods")
		assert.NotContains(t, m, "quarters")
	})

	t.Run("nilCalendarDatesOmitted", func(t *testing.T) {
		m := toMap(t, Calendar{})
		assert.NotContains(t, m, "basePeriodStart")
		assert.NotContains(t, m, "booksClosedThrough")
		assert.Equal(t, false, m["configured"])
	})

	t.Run("nilBooksClosedThroughIsNull", func(t *testing.T) {
		m := toMap(t, StatusChangeResult{})
		v, ok := m["booksClosedThrough"]
		assert.True(t, ok)
		assert.Nil(t, v)
	})
}

func TestFormatDatePtr(t *testing.T) {
	d := time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC)
	assert.Nil(t, formatDatePtr(nil))
	got := formatDatePtr(&d)
	require.NotNil(t, got)
	assert.Equal(t, "2026-12-31", *got)
}
