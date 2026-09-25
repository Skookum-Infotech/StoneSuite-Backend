package controllers

import (
	"container/list"
	"strings"
	"sync"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
)

// Help-answer cache tuning. Help chunks are tenant-independent (identical
// content for every tenant — see ai.HelpCorpus), so a cache hit for one
// tenant's caller is exactly as valid an answer for another's, provided the
// question, the help corpus content, and the chat model all match.
const (
	helpCacheMaxEntries = 200
	helpCacheTTL        = 1 * time.Hour
)

// helpCacheKey identifies one cacheable help answer. docsHash and chatModel
// are part of the key (not just the question) so a doc-corpus re-ingest or a
// model swap invalidates every cached answer automatically, rather than
// serving a stale answer indexed under a hash that's no longer meaningful.
type helpCacheKey struct {
	question  string
	docsHash  string
	chatModel string
}

// normalizeHelpCacheQuestion collapses a question to lowercase with
// whitespace runs collapsed to one space, so "How do I  export?" and "how do
// i export?" share a cache entry.
func normalizeHelpCacheQuestion(question string) string {
	return strings.Join(strings.Fields(strings.ToLower(question)), " ")
}

// helpCacheEntry is one cached answer plus its citations and expiry.
type helpCacheEntry struct {
	key       helpCacheKey
	answer    string
	citations []ragcore.Citation
	expiresAt time.Time
}

// helpCache is a small in-process LRU + TTL cache for intentHelp, first-turn
// answers (see runAskDispatch) — the one class of ask whose corpus, prompt,
// and content are identical across every caller in every tenant. now is
// injected so tests don't depend on wall-clock timing.
type helpCache struct {
	mu    sync.Mutex
	now   func() time.Time
	max   int
	ttl   time.Duration
	ll    *list.List
	items map[helpCacheKey]*list.Element
}

// newHelpCache builds a cache using now as its clock; pass nil for time.Now
// in production, or a fake clock in tests.
func newHelpCache(now func() time.Time) *helpCache {
	if now == nil {
		now = time.Now
	}
	return &helpCache{
		now: now, max: helpCacheMaxEntries, ttl: helpCacheTTL,
		ll: list.New(), items: make(map[helpCacheKey]*list.Element),
	}
}

// get returns the cached answer for key, if present and not expired. A hit
// moves the entry to the front (most-recently-used); an expired entry is
// evicted on the read that finds it, rather than waiting for a background
// sweep this small a cache doesn't need.
func (c *helpCache) get(key helpCacheKey) (answer string, citations []ragcore.Citation, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, found := c.items[key]
	if !found {
		return "", nil, false
	}
	entry := el.Value.(*helpCacheEntry)
	if c.now().After(entry.expiresAt) {
		c.ll.Remove(el)
		delete(c.items, key)
		return "", nil, false
	}
	c.ll.MoveToFront(el)
	return entry.answer, entry.citations, true
}

// set stores answer/citations for key, evicting the least-recently-used
// entry once the cache is over its max size.
func (c *helpCache) set(key helpCacheKey, answer string, citations []ragcore.Citation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, found := c.items[key]; found {
		entry := el.Value.(*helpCacheEntry)
		entry.answer, entry.citations = answer, citations
		entry.expiresAt = c.now().Add(c.ttl)
		c.ll.MoveToFront(el)
		return
	}
	el := c.ll.PushFront(&helpCacheEntry{key: key, answer: answer, citations: citations, expiresAt: c.now().Add(c.ttl)})
	c.items[key] = el
	if c.ll.Len() <= c.max {
		return
	}
	oldest := c.ll.Back()
	if oldest == nil {
		return
	}
	c.ll.Remove(oldest)
	delete(c.items, oldest.Value.(*helpCacheEntry).key)
}
