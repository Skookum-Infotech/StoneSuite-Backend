package main

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

// TestNeedsHelpResync exercises the pure decision function syncHelpCorpus
// uses: whether the help corpus must be re-ingested given the last recorded
// state (or its absence) and the current doc/embedder values.
func TestNeedsHelpResync(t *testing.T) {
	current := helpCorpusState{ContentHash: "hash-a", EmbedFingerprint: "model-a"}

	tests := []struct {
		name   string
		stored *helpCorpusState
		want   bool
	}{
		{"no state recorded yet", nil, true},
		{"identical state: up to date", &helpCorpusState{ContentHash: "hash-a", EmbedFingerprint: "model-a"}, false},
		{"doc content changed", &helpCorpusState{ContentHash: "hash-old", EmbedFingerprint: "model-a"}, true},
		{"embedder changed", &helpCorpusState{ContentHash: "hash-a", EmbedFingerprint: "model-old"}, true},
		{"both changed", &helpCorpusState{ContentHash: "hash-old", EmbedFingerprint: "model-old"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, needsHelpResync(tt.stored, current))
		})
	}
}
