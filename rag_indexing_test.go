package main

import (
	"strings"
	"testing"
	"time"
)

func TestReconcilePlan(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	later := t0.Add(time.Hour)

	indexed := map[string]indexedChunk{
		"current":  {updatedAt: later},
		"stale":    {updatedAt: t0},
		"untyped":  {updatedAt: later, untyped: true},
		"orphaned": {updatedAt: later},
	}
	live := map[string]time.Time{
		"current": t0,
		"stale":   later,
		"untyped": t0,
		"new":     t0,
	}

	tests := []struct {
		name        string
		complete    bool
		wantUpserts string
		wantDeletes string
	}{
		{"complete lists: refresh stale/untyped/new, delete orphans", true, "new,stale,untyped", "orphaned"},
		{"a failed list never deletes", false, "new,stale,untyped", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ups, dels := reconcilePlan(indexed, live, tt.complete)
			if strings.Join(ups, ",") != tt.wantUpserts || strings.Join(dels, ",") != tt.wantDeletes {
				t.Fatalf("upserts=%v deletes=%v", ups, dels)
			}
		})
	}
}
