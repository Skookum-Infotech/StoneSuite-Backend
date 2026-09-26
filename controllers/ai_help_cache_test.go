package controllers

import (
	"testing"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
)

// fakeClock is a simple injectable clock for helpCache tests.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestNormalizeHelpCacheQuestion(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already normalized", "how do i export", "how do i export"},
		{"mixed case", "How Do I Export?", "how do i export?"},
		{"extra whitespace collapsed", "how   do  i\texport", "how do i export"},
		{"leading/trailing whitespace trimmed", "  how do i export  ", "how do i export"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeHelpCacheQuestion(tt.in); got != tt.want {
				t.Errorf("normalizeHelpCacheQuestion(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHelpCache_GetSet(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c := newHelpCache(clock.now)
	key := helpCacheKey{question: "how do i export", docsHash: "h1", chatModel: "llama3.2:3b"}

	if _, _, ok := c.get(key); ok {
		t.Fatal("expected miss on an empty cache")
	}

	cites := []ragcore.Citation{{SourceType: "help", SourceID: "doc1"}}
	c.set(key, "Go to Settings > Export.", cites)

	answer, gotCites, ok := c.get(key)
	if !ok || answer != "Go to Settings > Export." || len(gotCites) != 1 {
		t.Fatalf("get() = (%q, %v, %v), want a hit", answer, gotCites, ok)
	}
}

func TestHelpCache_DifferentKeyPartsMiss(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	c := newHelpCache(clock.now)
	base := helpCacheKey{question: "q", docsHash: "h1", chatModel: "m1"}
	c.set(base, "answer", nil)

	tests := []struct {
		name string
		key  helpCacheKey
	}{
		{"different question", helpCacheKey{question: "q2", docsHash: "h1", chatModel: "m1"}},
		{"different docs hash", helpCacheKey{question: "q", docsHash: "h2", chatModel: "m1"}},
		{"different chat model", helpCacheKey{question: "q", docsHash: "h1", chatModel: "m2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, ok := c.get(tt.key); ok {
				t.Fatal("expected a miss: cache key must be the full (question, docsHash, chatModel) tuple")
			}
		})
	}
}

func TestHelpCache_TTLExpiry(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c := newHelpCache(clock.now)
	key := helpCacheKey{question: "q", docsHash: "h", chatModel: "m"}
	c.set(key, "answer", nil)

	clock.t = clock.t.Add(helpCacheTTL - time.Second)
	if _, _, ok := c.get(key); !ok {
		t.Fatal("expected a hit just before TTL expiry")
	}

	clock.t = clock.t.Add(2 * time.Second)
	if _, _, ok := c.get(key); ok {
		t.Fatal("expected a miss once TTL has expired")
	}
}

func TestHelpCache_EvictsLeastRecentlyUsedOverCapacity(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	c := newHelpCache(clock.now)
	c.max = 2

	k1 := helpCacheKey{question: "q1", docsHash: "h", chatModel: "m"}
	k2 := helpCacheKey{question: "q2", docsHash: "h", chatModel: "m"}
	k3 := helpCacheKey{question: "q3", docsHash: "h", chatModel: "m"}

	c.set(k1, "a1", nil)
	c.set(k2, "a2", nil)
	// Touch k1 so it's more recently used than k2.
	if _, _, ok := c.get(k1); !ok {
		t.Fatal("expected k1 to still be cached")
	}
	c.set(k3, "a3", nil) // over capacity: evicts the least-recently-used, k2

	if _, _, ok := c.get(k2); ok {
		t.Fatal("expected k2 to have been evicted as least-recently-used")
	}
	if _, _, ok := c.get(k1); !ok {
		t.Fatal("expected k1 to survive eviction (it was touched more recently)")
	}
	if _, _, ok := c.get(k3); !ok {
		t.Fatal("expected k3 (just inserted) to be present")
	}
}

func TestHelpCache_SetOverwritesAndRefreshesTTL(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	c := newHelpCache(clock.now)
	key := helpCacheKey{question: "q", docsHash: "h", chatModel: "m"}
	c.set(key, "first", nil)

	clock.t = clock.t.Add(30 * time.Minute)
	c.set(key, "second", nil) // refreshes expiresAt from the new "now"

	clock.t = clock.t.Add(helpCacheTTL - time.Minute)
	answer, _, ok := c.get(key)
	if !ok || answer != "second" {
		t.Fatalf("get() = (%q, %v), want (\"second\", true) — overwrite must refresh TTL", answer, ok)
	}
}
