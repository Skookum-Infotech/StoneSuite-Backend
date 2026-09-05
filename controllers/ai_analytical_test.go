package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/query"
)

func TestClassifyCountQuestion(t *testing.T) {
	tests := []struct {
		name     string
		question string
		wantKeys []string
		wantOK   bool
	}{
		{
			name:     "how many customers",
			question: "How many customers do we have?",
			wantKeys: []string{"customer"},
			wantOK:   true,
		},
		{
			name:     "how many leads",
			question: "how many leads",
			wantKeys: []string{"lead"},
			wantOK:   true,
		},
		{
			name:     "count of prospects",
			question: "count of prospects",
			wantKeys: []string{"prospect"},
			wantOK:   true,
		},
		{
			name:     "number of customers",
			question: "Number of customers?",
			wantKeys: []string{"customer"},
			wantOK:   true,
		},
		{
			name:     "how many CRM records",
			question: "how many CRM records do we have",
			wantKeys: []string{"lead", "prospect", "customer"}, // crmstore.CRMWorkflowKeys() stage order
			wantOK:   true,
		},
		{
			name:     "how many records",
			question: "How many records do we have",
			wantKeys: []string{"lead", "prospect", "customer"}, // crmstore.CRMWorkflowKeys() stage order
			wantOK:   true,
		},
		{
			name:     "no count intent -> fall through",
			question: "Tell me about crm",
			wantOK:   false,
		},
		{
			name:     "date filter with no count intent -> fall through",
			question: "which customer won in last 1 week",
			wantOK:   false,
		},
		{
			name:     "count intent + filter hint word -> fall through (critical guard)",
			question: "how many customers won last week",
			wantOK:   false,
		},
		{
			name:     "count intent + closed hint -> fall through",
			question: "how many customers closed this month",
			wantOK:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, ok := classifyCountQuestion(tt.question)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (keys=%v)", ok, tt.wantOK, keys)
			}
			if !ok {
				return
			}
			if len(keys) != len(tt.wantKeys) {
				t.Fatalf("keys = %v, want %v", keys, tt.wantKeys)
			}
			for i, k := range tt.wantKeys {
				if keys[i] != k {
					t.Fatalf("keys = %v, want %v", keys, tt.wantKeys)
				}
			}
		})
	}
}

func TestFormatCountAnswer(t *testing.T) {
	tests := []struct {
		name  string
		keys  []string
		count map[string]int
		total int
		want  string
	}{
		{
			name:  "single key singular",
			keys:  []string{"customer"},
			count: map[string]int{"customer": 1},
			total: 1,
			want:  "You have 1 customer.",
		},
		{
			name:  "single key plural",
			keys:  []string{"lead"},
			count: map[string]int{"lead": 5},
			total: 5,
			want:  "You have 5 leads.",
		},
		{
			name:  "multiple keys with total",
			keys:  []string{"customer", "lead", "prospect"},
			count: map[string]int{"customer": 2, "lead": 3, "prospect": 0},
			total: 5,
			want:  "You have 2 customers, 3 leads, 0 prospects (5 CRM records total).",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCountAnswer(tt.keys, tt.count, tt.total)
			if got != tt.want {
				t.Fatalf("formatCountAnswer() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPluralize(t *testing.T) {
	tests := []struct {
		key  string
		n    int
		want string
	}{
		{"customer", 1, "customer"},
		{"customer", 0, "customers"},
		{"customer", 2, "customers"},
		{"lead", 1, "lead"},
		{"lead", 5, "leads"},
	}
	for _, tt := range tests {
		got := pluralize(tt.key, tt.n)
		if got != tt.want {
			t.Fatalf("pluralize(%q, %d) = %q, want %q", tt.key, tt.n, got, tt.want)
		}
	}
}

// fakeCountStore is a minimal crmstore.Store test double: it embeds Store for
// every method this test doesn't exercise (a nil call to any of them would
// panic, which is fine — these tests only call CountRecords), and overrides
// CountRecords to return canned results per key. Mirrors the fakeLoaderStore
// pattern in crmstore/rag_loader_test.go.
type fakeCountStore struct {
	crmstore.Store
	counts        map[string]int
	err           error
	errOnly       string // if set, only this key errors; other keys still return counts
	calls         []string
	filteredCalls []filteredCall
}

func (f *fakeCountStore) CountRecords(_ context.Context, _ *pgxpool.Pool, key, _, _ string) (int, error) {
	f.calls = append(f.calls, key)
	if f.err != nil && (f.errOnly == "" || f.errOnly == key) {
		return 0, f.err
	}
	return f.counts[key], nil
}

// filteredCall records one CountRecordsFiltered invocation for assertions —
// in particular that scope/actorIdentityID reach the store unaltered by
// anything the LLM router returned (see TestResolveRoutedFilteredCount_*).
type filteredCall struct {
	key             string
	scope           string
	actorIdentityID string
	filters         []query.Clause
}

func (f *fakeCountStore) CountRecordsFiltered(_ context.Context, _ *pgxpool.Pool, key, scope, actorIdentityID string, filters []query.Clause) (int, error) {
	f.filteredCalls = append(f.filteredCalls, filteredCall{key, scope, actorIdentityID, filters})
	if f.err != nil && (f.errOnly == "" || f.errOnly == key) {
		return 0, f.err
	}
	return f.counts[key], nil
}

func TestCountCRMRecords_SumsAcrossKeysAndFormatsAnswer(t *testing.T) {
	t.Run("single key", func(t *testing.T) {
		store := &fakeCountStore{counts: map[string]int{"customer": 7}}
		res, err := countCRMRecords(context.Background(), store, nil, "all", "identity-1", []string{"customer"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Answer != "You have 7 customers." {
			t.Fatalf("Answer = %q", res.Answer)
		}
		if res.Citations == nil || len(res.Citations) != 0 {
			t.Fatalf("Citations = %#v, want non-nil empty slice", res.Citations)
		}
	})

	t.Run("multiple keys", func(t *testing.T) {
		store := &fakeCountStore{counts: map[string]int{"customer": 2, "lead": 3, "prospect": 1}}
		res, err := countCRMRecords(context.Background(), store, nil, "own", "identity-1",
			[]string{"customer", "lead", "prospect"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "You have 2 customers, 3 leads, 1 prospect (6 CRM records total)."
		if res.Answer != want {
			t.Fatalf("Answer = %q, want %q", res.Answer, want)
		}
		if res.Citations == nil || len(res.Citations) != 0 {
			t.Fatalf("Citations = %#v, want non-nil empty slice", res.Citations)
		}
		if len(store.calls) != 3 {
			t.Fatalf("calls = %v, want 3 CountRecords calls", store.calls)
		}
	})
}

func TestCountCRMRecords_PropagatesStoreError(t *testing.T) {
	store := &fakeCountStore{err: errBoomAnalytical}
	_, err := countCRMRecords(context.Background(), store, nil, "all", "identity-1", []string{"customer"})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

// errBoomAnalytical is a local sentinel test error (this package has no
// shared errBoom helper the way crmstore's tests do).
var errBoomAnalytical = errors.New("boom")

// fakeStructuredLLM is a rag.LLMClient + rag.StructuredLLMClient test double
// for route.Extract: ChatJSON returns a fixed response (or error) and records
// what it was asked.
type fakeStructuredLLM struct {
	response string
	err      error
}

func (f *fakeStructuredLLM) Chat(context.Context, string, []ragcore.Message) (string, error) {
	return "", errors.New("Chat should not be called by the routing path")
}

func (f *fakeStructuredLLM) ChatJSON(context.Context, string, []ragcore.Message, []byte) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.response, nil
}

func TestHasFilterHintCountIntent(t *testing.T) {
	tests := []struct {
		name     string
		question string
		want     bool
	}{
		{"plain count, no filter hint", "how many leads do we have", false},
		{"filtered count with a type word", "how many leads closed last quarter", true},
		{"filtered count with the generic record word", "how many records were qualified this week", true},
		{"no count intent at all", "tell me about Acme Corp", false},
		{"count intent but unrelated to CRM records", "how many angels dance on a pin last week", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasFilterHintCountIntent(tt.question); got != tt.want {
				t.Errorf("hasFilterHintCountIntent(%q) = %v, want %v", tt.question, got, tt.want)
			}
		})
	}
}

func TestResolveRoutedFilteredCount_UnsupportedLLMFallsBack(t *testing.T) {
	llm := &ragcore.FakeLLM{Reply: "unused"} // implements Chat only, not ChatJSON
	store := &fakeCountStore{}
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "own", "identity-1", "how many leads closed last week")
	if ok {
		t.Fatal("expected fallback (ok=false) for an LLM without structured output support")
	}
	if len(store.filteredCalls) != 0 {
		t.Fatalf("store should not be called on fallback, got %v", store.filteredCalls)
	}
}

func TestResolveRoutedFilteredCount_HappyPath(t *testing.T) {
	llm := &fakeStructuredLLM{response: `{
		"intent": "count",
		"workflow_keys": ["lead"],
		"filters": [{"field": "status", "op": "eq", "value": "qualified"}],
		"search_text": ""
	}`}
	store := &fakeCountStore{counts: map[string]int{"lead": 4}}

	res, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "own", "identity-1", "how many leads are qualified this week")
	if !ok {
		t.Fatal("expected the routed count path to succeed")
	}
	if res.Answer != "You have 4 leads." {
		t.Fatalf("Answer = %q", res.Answer)
	}
	if len(store.filteredCalls) != 1 {
		t.Fatalf("expected exactly 1 CountRecordsFiltered call, got %d", len(store.filteredCalls))
	}
	call := store.filteredCalls[0]
	if call.key != "lead" || call.scope != "own" || call.actorIdentityID != "identity-1" {
		t.Fatalf("call = %+v, want key=lead scope=own actorIdentityID=identity-1", call)
	}
	if len(call.filters) != 1 || call.filters[0] != (query.Clause{Field: "status", Op: query.OpEq, Value: "qualified"}) {
		t.Fatalf("filters = %+v, want one status=qualified eq filter", call.filters)
	}
}

func TestResolveRoutedFilteredCount_NoWorkflowKeysCountsAll(t *testing.T) {
	llm := &fakeStructuredLLM{response: `{"intent": "count", "workflow_keys": [], "filters": [], "search_text": ""}`}
	store := &fakeCountStore{counts: map[string]int{"lead": 1, "prospect": 2, "customer": 3}}

	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "all", "identity-1", "how many records were touched last week")
	if !ok {
		t.Fatal("expected success")
	}
	if len(store.filteredCalls) != len(crmstore.CRMWorkflowKeys()) {
		t.Fatalf("expected one call per CRM workflow key when the model names none, got %d", len(store.filteredCalls))
	}
}

func TestResolveRoutedFilteredCount_NonCountIntentFallsBack(t *testing.T) {
	llm := &fakeStructuredLLM{response: `{"intent": "search", "workflow_keys": [], "filters": [], "search_text": "x"}`}
	store := &fakeCountStore{}
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "own", "identity-1", "tell me about last week")
	if ok {
		t.Fatal("expected fallback for a non-count intent")
	}
}

func TestResolveRoutedFilteredCount_MalformedModelOutputFallsBack(t *testing.T) {
	llm := &fakeStructuredLLM{response: `not json`}
	store := &fakeCountStore{}
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "own", "identity-1", "how many leads last week")
	if ok {
		t.Fatal("expected fallback for malformed model output")
	}
	if len(store.filteredCalls) != 0 {
		t.Fatal("store must not be called when the model output is malformed")
	}
}

func TestResolveRoutedFilteredCount_UnknownWorkflowKeyFallsBack(t *testing.T) {
	llm := &fakeStructuredLLM{response: `{"intent": "count", "workflow_keys": ["invoice"], "filters": [], "search_text": ""}`}
	store := &fakeCountStore{counts: map[string]int{"lead": 1}}
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "own", "identity-1", "how many invoices last week")
	if ok {
		t.Fatal("expected fallback for a workflow key that is not a real CRM key")
	}
	if len(store.filteredCalls) != 0 {
		t.Fatal("store must not be called when a workflow key is invalid")
	}
}

// TestResolveRoutedFilteredCount_DisallowedFieldFallsBack is the core
// security property test: even if the model (whether confused or steered by
// indirect prompt injection in a record's content) names a field outside
// routeFieldWhitelist — including identity-shaped fields like owner_user_id —
// the routed path refuses rather than building a query around it. The model
// chooses WHAT to filter on; it must never get to choose WHOSE data, and
// owner_user_id is exactly that kind of field.
func TestResolveRoutedFilteredCount_DisallowedFieldFallsBack(t *testing.T) {
	for _, field := range []string{"owner_user_id", "team_id", "id", "cf:secret_field"} {
		t.Run(field, func(t *testing.T) {
			llm := &fakeStructuredLLM{response: `{
				"intent": "count",
				"workflow_keys": ["lead"],
				"filters": [{"field": "` + field + `", "op": "eq", "value": "x"}],
				"search_text": ""
			}`}
			store := &fakeCountStore{counts: map[string]int{"lead": 1}}
			_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "all", "identity-1", "how many leads last week")
			if ok {
				t.Fatalf("expected fallback for filter field %q outside the whitelist", field)
			}
			if len(store.filteredCalls) != 0 {
				t.Fatalf("store must not be called when a filter field is disallowed (field=%q)", field)
			}
		})
	}
}

func TestResolveRoutedFilteredCount_DisallowedOperatorFallsBack(t *testing.T) {
	llm := &fakeStructuredLLM{response: `{
		"intent": "count",
		"workflow_keys": ["lead"],
		"filters": [{"field": "status", "op": "in", "value": "x"}],
		"search_text": ""
	}`}
	store := &fakeCountStore{counts: map[string]int{"lead": 1}}
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "all", "identity-1", "how many leads last week")
	if ok {
		t.Fatal("expected fallback for an operator outside routeOperatorSet (OpIn needs multi-value input this path doesn't offer)")
	}
	if len(store.filteredCalls) != 0 {
		t.Fatal("store must not be called when an operator is disallowed")
	}
}

func TestResolveRoutedFilteredCount_StoreErrorFallsBack(t *testing.T) {
	llm := &fakeStructuredLLM{response: `{"intent": "count", "workflow_keys": ["lead"], "filters": [], "search_text": ""}`}
	store := &fakeCountStore{err: errBoomAnalytical}
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, "own", "identity-1", "how many leads last week")
	if ok {
		t.Fatal("expected fallback when the store call itself fails")
	}
}
