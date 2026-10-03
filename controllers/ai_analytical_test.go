package controllers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
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
		{
			name:     "two count clauses -> union of both types",
			question: "How many customers and how many leads?",
			wantKeys: []string{"customer", "lead"},
			wantOK:   true,
		},
		{
			name:     "two count clauses, different phrasing",
			question: "number of leads, and how many prospects do we have",
			wantKeys: []string{"lead", "prospect"},
			wantOK:   true,
		},
		{
			name:     "repeated type across clauses is counted once",
			question: "how many leads and how many leads",
			wantKeys: []string{"lead"},
			wantOK:   true,
		},
		{
			name:     "trailing 'in total' is not a second clause",
			question: "how many customers in total",
			wantKeys: []string{"customer"},
			wantOK:   true,
		},
		{
			name:     "second clause we cannot answer -> whole question falls through",
			question: "how many customers and how many emails did they send",
			wantOK:   false,
		},
		{
			name:     "weak phrase directly before a strong one is one clause",
			question: "What is the total number of leads?",
			wantKeys: []string{"lead"},
			wantOK:   true,
		},
		{
			name:     "count phrases with no object at all -> fall through",
			question: "how many, how many",
			wantOK:   false,
		},
		{
			name:     "filter word in either clause -> fall through",
			question: "how many customers and how many leads won last week",
			wantOK:   false,
		},
		{
			name:     "qualifier after the type word -> fall through",
			question: "how many customers in Texas",
			wantOK:   false,
		},
		{
			name:     "qualifier naming a person -> fall through",
			question: "how many leads does John have",
			wantOK:   false,
		},
		{
			name:     "trailing 'do I have' is harmless",
			question: "how many customers do I have",
			wantKeys: []string{"customer"},
			wantOK:   true,
		},
		{
			name:     "trailing 'are there' is harmless",
			question: "how many customers are there",
			wantKeys: []string{"customer"},
			wantOK:   true,
		},
		{
			name:     "trailing 'exist' is harmless",
			question: "how many customers exist",
			wantKeys: []string{"customer"},
			wantOK:   true,
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
		name       string
		keys       []string
		count      map[string]int
		total      int
		filterDesc string
		want       string
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
		{
			name:       "single key with filter description",
			keys:       []string{"lead"},
			count:      map[string]int{"lead": 3},
			total:      3,
			filterDesc: "status New, created since 2026-09-01",
			want:       "You have 3 leads with status New, created since 2026-09-01.",
		},
		{
			name:       "multiple keys with filter description",
			keys:       []string{"customer", "lead"},
			count:      map[string]int{"customer": 1, "lead": 2},
			total:      3,
			filterDesc: "status New",
			want:       "You have 1 customer, 2 leads with status New (3 CRM records total).",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatCountAnswer(tt.keys, tt.count, tt.total, tt.filterDesc)
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
	scopes        []string // "key:scope" per CountRecords call
	filteredCalls []filteredCall
	// statuses is returned by AllStatuses — the source resolveRoutedFilteredCount
	// uses to build route.WithAllowedValues and map a status name back to its
	// id (see statusAllowedValues). Empty by default: no status allowed-values
	// offered, matching resolveRoutedFilteredCount's pre-Phase-6b behavior.
	statuses []workflow.StatusInfo
	// statusesErr, when set, is returned by Statuses (not AllStatuses) — the
	// countCRMRecordsOpenClosed error-propagation path.
	statusesErr error
}

// AllStatuses satisfies crmstore.Store for resolveRoutedFilteredCount's
// status-name-to-id lookup (see statusAllowedValues); every other test double
// in this file that never sets f.statuses gets an empty list back, which
// resolveRoutedFilteredCount treats as "offer no status allowed-values" —
// not a fallback.
func (f *fakeCountStore) AllStatuses(context.Context, *pgxpool.Pool) ([]workflow.StatusInfo, error) {
	return f.statuses, nil
}

// Statuses satisfies crmstore.Store for countCRMRecordsOpenClosed's
// terminal/non-terminal lookup — f.statuses filtered to one workflow key,
// matching the real store's per-key scoping (unlike AllStatuses above, which
// returns every workflow's statuses unfiltered).
func (f *fakeCountStore) Statuses(_ context.Context, _ *pgxpool.Pool, key string) ([]workflow.StatusInfo, error) {
	if f.statusesErr != nil {
		return nil, f.statusesErr
	}
	var out []workflow.StatusInfo
	for _, s := range f.statuses {
		if s.WorkflowKey == key {
			out = append(out, s)
		}
	}
	return out, nil
}

// allGrants / ownGrants read every CRM type at one scope — the shape every
// caller had before grants became per type.
var (
	allGrants = ai.Grants{"lead": ai.ScopeAll, "prospect": ai.ScopeAll, "customer": ai.ScopeAll}
	ownGrants = ai.Grants{"lead": ai.ScopeOwn, "prospect": ai.ScopeOwn, "customer": ai.ScopeOwn}
)

func (f *fakeCountStore) CountRecords(_ context.Context, _ *pgxpool.Pool, key, scope, _ string) (int, error) {
	f.calls = append(f.calls, key)
	f.scopes = append(f.scopes, key+":"+scope)
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
		res, err := countCRMRecords(context.Background(), store, nil, allGrants, "identity-1", []string{"customer"})
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
		res, err := countCRMRecords(context.Background(), store, nil, ownGrants, "identity-1",
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
	_, err := countCRMRecords(context.Background(), store, nil, allGrants, "identity-1", []string{"customer"})
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

// TestCountCRMRecords_PerTypeGrants is the count half of the per-type scope
// fix: each type is counted under its OWN scope, ungranted types are never
// counted (not even as zero), and the answer names what was left out.
func TestCountCRMRecords_PerTypeGrants(t *testing.T) {
	counts := map[string]int{"customer": 9, "lead": 3, "prospect": 1}

	t.Run("each type under its own scope", func(t *testing.T) {
		store := &fakeCountStore{counts: counts}
		grants := ai.Grants{"lead": ai.ScopeAll, "customer": ai.ScopeOwn}
		if _, err := countCRMRecords(context.Background(), store, nil, grants, "identity-1", []string{"customer", "lead"}); err != nil {
			t.Fatal(err)
		}
		if strings.Join(store.scopes, ",") != "customer:own,lead:all" {
			t.Fatalf("scopes = %v", store.scopes)
		}
	})

	t.Run("only an ungranted type: no number, no store call", func(t *testing.T) {
		store := &fakeCountStore{counts: counts}
		res, err := countCRMRecords(context.Background(), store, nil, ai.Grants{"lead": ai.ScopeAll}, "identity-1", []string{"customer"})
		if err != nil {
			t.Fatal(err)
		}
		if res.Answer != "You don't have access to customer records." || len(store.calls) != 0 {
			t.Fatalf("answer %q after calls %v", res.Answer, store.calls)
		}
	})

	t.Run("mixed: counts the granted, names the rest", func(t *testing.T) {
		store := &fakeCountStore{counts: counts}
		res, err := countCRMRecords(context.Background(), store, nil, ai.Grants{"lead": ai.ScopeAll}, "identity-1", []string{"customer", "lead", "prospect"})
		if err != nil {
			t.Fatal(err)
		}
		want := "You have 3 leads. You don't have access to customer or prospect records."
		if res.Answer != want || strings.Join(store.calls, ",") != "lead" {
			t.Fatalf("answer %q (want %q) after calls %v", res.Answer, want, store.calls)
		}
	})

	t.Run("unknown scope value is not a grant", func(t *testing.T) {
		store := &fakeCountStore{counts: counts}
		res, _ := countCRMRecords(context.Background(), store, nil, ai.Grants{"lead": "team"}, "identity-1", []string{"lead"})
		if len(store.calls) != 0 || !strings.Contains(res.Answer, "don't have access") {
			t.Fatalf("a retired/unknown scope must fail closed: %q after %v", res.Answer, store.calls)
		}
	})
}

func TestResolveRoutedFilteredCount_NoGrantsSkipsModelCall(t *testing.T) {
	llm := &fakeStructuredLLM{err: errors.New("must not be called")}
	store := &fakeCountStore{}
	if _, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, ai.Grants{}, "identity-1", "how many leads closed last week", nil); ok {
		t.Fatal("no grants must fall back without routing")
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
		{"filter adjective before the type still routes", "how many qualified leads do we have", true},
		{"type word that isn't the counted object", "how many emails did the lead send last week", false},
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
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, ownGrants, "identity-1", "how many leads closed last week", nil)
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
	store := &fakeCountStore{
		counts:   map[string]int{"lead": 4},
		statuses: []workflow.StatusInfo{{WorkflowKey: "lead", StatusLabel: "qualified", StateID: "42"}},
	}

	res, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, ownGrants, "identity-1", "how many leads are qualified this week", nil)
	if !ok {
		t.Fatal("expected the routed count path to succeed")
	}
	if res.Answer != "You have 4 leads with status qualified." {
		t.Fatalf("Answer = %q", res.Answer)
	}
	if len(store.filteredCalls) != 1 {
		t.Fatalf("expected exactly 1 CountRecordsFiltered call, got %d", len(store.filteredCalls))
	}
	call := store.filteredCalls[0]
	if call.key != "lead" || call.scope != "own" || call.actorIdentityID != "identity-1" {
		t.Fatalf("call = %+v, want key=lead scope=own actorIdentityID=identity-1", call)
	}
	// The filter's value is the status ID ("42"), resolved from the model's
	// status NAME ("qualified") via statusAllowedValues — never the raw name,
	// which the store's column doesn't hold (see relationalSystemFields).
	if len(call.filters) != 1 || call.filters[0] != (query.Clause{Field: "status", Op: query.OpEq, Value: "42"}) {
		t.Fatalf("filters = %+v, want one status=42 eq filter", call.filters)
	}
}

// TestResolveRoutedFilteredCount_NoFilterFallsBack locks in the Phase 6b
// behavior: a question only routed here because it carried a filter-hint word
// (hasFilterHintCountIntent), where the model then extracted neither a
// workflow key nor a filter (Route.NoFilter), is a FAILED extraction — not
// "count every type unfiltered". Before this fix the same model response
// (empty workflow_keys, empty filters) silently returned the unfiltered
// total, mislabeled as the answer to a filtered question.
func TestResolveRoutedFilteredCount_NoFilterFallsBack(t *testing.T) {
	llm := &fakeStructuredLLM{response: `{"intent": "count", "workflow_keys": [], "filters": [], "search_text": ""}`}
	store := &fakeCountStore{counts: map[string]int{"lead": 1, "prospect": 2, "customer": 3}}

	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, allGrants, "identity-1", "how many records were touched last week", nil)
	if ok {
		t.Fatal("expected fallback: Route.NoFilter must never be read as an unfiltered count")
	}
	if len(store.filteredCalls) != 0 {
		t.Fatalf("store must not be called when the router extracted no filter, got %v", store.filteredCalls)
	}
}

func TestResolveRoutedFilteredCount_NonCountIntentFallsBack(t *testing.T) {
	llm := &fakeStructuredLLM{response: `{"intent": "search", "workflow_keys": [], "filters": [], "search_text": "x"}`}
	store := &fakeCountStore{}
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, ownGrants, "identity-1", "tell me about last week", nil)
	if ok {
		t.Fatal("expected fallback for a non-count intent")
	}
}

func TestResolveRoutedFilteredCount_MalformedModelOutputFallsBack(t *testing.T) {
	llm := &fakeStructuredLLM{response: `not json`}
	store := &fakeCountStore{}
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, ownGrants, "identity-1", "how many leads last week", nil)
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
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, ownGrants, "identity-1", "how many invoices last week", nil)
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
			_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, allGrants, "identity-1", "how many leads last week", nil)
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
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, allGrants, "identity-1", "how many leads last week", nil)
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
	_, ok := resolveRoutedFilteredCount(context.Background(), llm, store, nil, ownGrants, "identity-1", "how many leads last week", nil)
	if ok {
		t.Fatal("expected fallback when the store call itself fails")
	}
}

func TestClassifyOpenClosedCount(t *testing.T) {
	tests := []struct {
		name     string
		question string
		wantKeys []string
		wantOpen bool
		wantOK   bool
	}{
		{
			name:     "how many open leads",
			question: "How many open leads do I have?",
			wantKeys: []string{"lead"},
			wantOpen: true,
			wantOK:   true,
		},
		{
			name:     "how many active customers",
			question: "how many active customers",
			wantKeys: []string{"customer"},
			wantOpen: true,
			wantOK:   true,
		},
		{
			name:     "how many closed prospects",
			question: "how many closed prospects",
			wantKeys: []string{"prospect"},
			wantOpen: false,
			wantOK:   true,
		},
		{
			name:     "two types, both open",
			question: "how many open leads and how many open prospects",
			wantKeys: []string{"lead", "prospect"},
			wantOpen: true,
			wantOK:   true,
		},
		{
			name:     "no count intent -> fall through",
			question: "tell me about my open leads",
			wantOK:   false,
		},
		{
			name:     "no filter hint at all -> fall through (classifyCountQuestion's job)",
			question: "how many leads",
			wantOK:   false,
		},
		{
			name:     "mixed open and closed -> ambiguous, fall through",
			question: "how many open and closed leads",
			wantOK:   false,
		},
		{
			name:     "open combined with a date filter -> fall through",
			question: "how many open leads last week",
			wantOK:   false,
		},
		{
			name:     "open combined with a literal status word -> fall through",
			question: "how many open qualified leads",
			wantOK:   false,
		},
		{
			name:     "module type -> fall through (no IsTerminal concept here)",
			question: "how many open invoices",
			wantOK:   false,
		},
		{
			name:     "a filter-hint word this path doesn't know -> fall through",
			question: "how many pending leads",
			wantOK:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys, open, ok := classifyOpenClosedCount(tt.question)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (keys=%v open=%v)", ok, tt.wantOK, keys, open)
			}
			if !ok {
				return
			}
			if open != tt.wantOpen {
				t.Fatalf("open = %v, want %v", open, tt.wantOpen)
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

// leadStatuses is a realistic lead status catalog (see workflow/seed.go):
// New/In Progress/Qualified are non-terminal, UnQualified/Converted/Dead are
// terminal — mirrors the actual seeded set countCRMRecordsOpenClosed's fix
// was designed against.
var leadStatuses = []workflow.StatusInfo{
	{StateID: "s-new", WorkflowKey: "lead", StatusLabel: "New", IsTerminal: false},
	{StateID: "s-inprogress", WorkflowKey: "lead", StatusLabel: "In Progress", IsTerminal: false},
	{StateID: "s-qualified", WorkflowKey: "lead", StatusLabel: "Qualified", IsTerminal: false},
	{StateID: "s-unqualified", WorkflowKey: "lead", StatusLabel: "UnQualified", IsTerminal: true},
	{StateID: "s-converted", WorkflowKey: "lead", StatusLabel: "Converted", IsTerminal: true},
	{StateID: "s-dead", WorkflowKey: "lead", StatusLabel: "Dead", IsTerminal: true},
}

func TestClassifySumQuestion(t *testing.T) {
	tests := []struct {
		name     string
		question string
		wantKey  string
		wantOK   bool
	}{
		{
			name:     "total outstanding balance across all invoices",
			question: "What is the total outstanding balance across all invoices?",
			wantKey:  "invoice",
			wantOK:   true,
		},
		{
			name:     "how much is outstanding on invoices",
			question: "How much is outstanding on invoices?",
			wantKey:  "invoice",
			wantOK:   true,
		},
		{
			name:     "sum of invoice balances",
			question: "what's the sum of invoice balances",
			wantKey:  "invoice",
			wantOK:   true,
		},
		{
			name:     "total balance due on invoices",
			question: "total balance due on invoices",
			wantKey:  "invoice",
			wantOK:   true,
		},
		{
			name:     "no sum intent -> fall through",
			question: "which invoices are overdue",
			wantOK:   false,
		},
		{
			name:     "sum intent but no money-field word -> fall through",
			question: "what is the total number of invoices",
			wantOK:   false,
		},
		{
			name:     "money-field word but no module named -> fall through",
			question: "what is my total balance",
			wantOK:   false,
		},
		{
			name:     "money-field word naming a different module -> fall through",
			question: "total outstanding balance on vendor bills",
			wantOK:   false,
		},
		{
			name:     "filter-hint word present -> a filtered sum, out of scope",
			question: "total outstanding balance on invoices this month",
			wantOK:   false,
		},
		{
			name:     "filter-hint word present (status) -> out of scope",
			question: "total outstanding balance on open invoices",
			wantOK:   false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, ok := classifySumQuestion(tt.question)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (key=%q)", ok, tt.wantOK, key)
			}
			if ok && key != tt.wantKey {
				t.Fatalf("key = %q, want %q", key, tt.wantKey)
			}
		})
	}
}

func TestSumModuleRecords_NoGrantSkipsHookCall(t *testing.T) {
	res, err := sumModuleRecords(context.Background(), nil, ai.Grants{}, "identity-1", "invoice")
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "You don't have access to invoice records." {
		t.Fatalf("answer = %q", res.Answer)
	}
}

func TestCountCRMRecordsOpenClosed_FiltersByIsTerminal(t *testing.T) {
	t.Run("open counts only non-terminal statuses", func(t *testing.T) {
		store := &fakeCountStore{statuses: leadStatuses, counts: map[string]int{"lead": 3}}
		res, err := countCRMRecordsOpenClosed(context.Background(), store, nil, allGrants, "identity-1", []string{"lead"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(store.filteredCalls) != 1 {
			t.Fatalf("filteredCalls = %v, want 1 call", store.filteredCalls)
		}
		call := store.filteredCalls[0]
		if len(call.filters) != 1 || call.filters[0].Field != "status" || call.filters[0].Op != query.OpIn {
			t.Fatalf("filters = %v, want one status/in clause", call.filters)
		}
		ids, ok := call.filters[0].Value.([]any)
		if !ok || len(ids) != 3 {
			t.Fatalf("filter value = %v, want the 3 non-terminal status ids", call.filters[0].Value)
		}
		for _, want := range []any{"s-new", "s-inprogress", "s-qualified"} {
			found := false
			for _, id := range ids {
				if id == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("ids = %v, missing non-terminal id %v", ids, want)
			}
		}
		want := "You have 3 leads with an open status."
		if res.Answer != want {
			t.Fatalf("answer = %q, want %q", res.Answer, want)
		}
	})

	t.Run("closed counts only terminal statuses", func(t *testing.T) {
		store := &fakeCountStore{statuses: leadStatuses, counts: map[string]int{"lead": 1}}
		res, err := countCRMRecordsOpenClosed(context.Background(), store, nil, allGrants, "identity-1", []string{"lead"}, false)
		if err != nil {
			t.Fatal(err)
		}
		ids, ok := store.filteredCalls[0].filters[0].Value.([]any)
		if !ok || len(ids) != 3 {
			t.Fatalf("filter value = %v, want the 3 terminal status ids", store.filteredCalls[0].filters[0].Value)
		}
		for _, want := range []any{"s-unqualified", "s-converted", "s-dead"} {
			found := false
			for _, id := range ids {
				if id == want {
					found = true
				}
			}
			if !found {
				t.Fatalf("ids = %v, missing terminal id %v", ids, want)
			}
		}
		want := "You have 1 lead with a closed status."
		if res.Answer != want {
			t.Fatalf("answer = %q, want %q", res.Answer, want)
		}
	})

	t.Run("no status of the requested polarity -> zero, no store filter call", func(t *testing.T) {
		allTerminal := []workflow.StatusInfo{{StateID: "s-x", WorkflowKey: "lead", IsTerminal: true}}
		store := &fakeCountStore{statuses: allTerminal}
		res, err := countCRMRecordsOpenClosed(context.Background(), store, nil, allGrants, "identity-1", []string{"lead"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if len(store.filteredCalls) != 0 {
			t.Fatalf("filteredCalls = %v, want none (nothing to filter for)", store.filteredCalls)
		}
		if res.Answer != "You have 0 leads with an open status." {
			t.Fatalf("answer = %q", res.Answer)
		}
	})

	t.Run("Statuses error propagates", func(t *testing.T) {
		store := &fakeCountStore{statusesErr: errBoomAnalytical}
		_, err := countCRMRecordsOpenClosed(context.Background(), store, nil, allGrants, "identity-1", []string{"lead"}, true)
		if err == nil {
			t.Fatal("expected the Statuses error to propagate")
		}
	})

	t.Run("scope/grants pass through exactly as an ordinary count", func(t *testing.T) {
		store := &fakeCountStore{statuses: leadStatuses, counts: map[string]int{"lead": 2}}
		grants := ai.Grants{"lead": ai.ScopeOwn}
		if _, err := countCRMRecordsOpenClosed(context.Background(), store, nil, grants, "identity-1", []string{"lead"}, true); err != nil {
			t.Fatal(err)
		}
		if store.filteredCalls[0].scope != "own" || store.filteredCalls[0].actorIdentityID != "identity-1" {
			t.Fatalf("filteredCall = %+v", store.filteredCalls[0])
		}
	})
}
