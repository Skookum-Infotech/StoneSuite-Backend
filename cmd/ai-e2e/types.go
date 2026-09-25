// Command ai-e2e evaluates the StoneSuite Assistant end-to-end against a
// deployed backend: it drives POST /api/tenant/ai/ask/stream through a set of
// scripted cases, checks each answer against expectations, and reports pass
// rate and latency percentiles.
//
// Usage:
//
//	go run ./cmd/ai-e2e -token-file /path/to/token.txt
//
// See -h for flags. Never prints the bearer token.
package main

import "time"

// testCase is one scripted question and its pass/fail expectations, as read
// from the cases JSON file.
type testCase struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Category string `json:"category"`

	ExpectContains    []string `json:"expect_contains,omitempty"`
	ExpectAny         []string `json:"expect_any,omitempty"`
	ExpectNotContains []string `json:"expect_not_contains,omitempty"`
	ExpectRefusal     bool     `json:"expect_refusal,omitempty"`
	ExpectStatus      int      `json:"expect_status,omitempty"`
	ExpectRoute       string   `json:"expect_route,omitempty"`
	ExpectMinSources  int      `json:"expect_min_sources,omitempty"`

	// FollowUpOf names another case's id whose conversation_id this case
	// reuses, for multi-turn cases. Resolved at run time, not at load time
	// (the referenced case must run first).
	FollowUpOf string `json:"follow_up_of,omitempty"`
}

// askResult is what one POST /api/tenant/ai/ask/stream call produced,
// independent of whether it satisfied its case's expectations.
type askResult struct {
	HTTPStatus     int
	Answer         string
	Done           map[string]any
	ErrorPayload   map[string]any
	TTFT           time.Duration
	Total          time.Duration
	ConversationID string
	Route          string
	SourcesCount   int
	TransportErr   error
	StatusBodyRaw  map[string]any
}

// caseResult is one case's outcome: its scripted expectations, the raw
// response, and the verdict — this is what gets written to -out and
// summarized in the table/report.
type caseResult struct {
	Case        testCase  `json:"case"`
	Result      askResult `json:"-"`
	Pass        bool      `json:"pass"`
	FirstFail   string    `json:"first_fail,omitempty"`
	TTFTMillis  int64     `json:"ttft_ms"`
	TotalMillis int64     `json:"total_ms"`
	Answer      string    `json:"answer"`
	HTTPStatus  int       `json:"http_status"`
	Done        any       `json:"done,omitempty"`
	Error       any       `json:"error,omitempty"`
}
