package controllers

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/crmstore"
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
	counts  map[string]int
	err     error
	errOnly string // if set, only this key errors; other keys still return counts
	calls   []string
}

func (f *fakeCountStore) CountRecords(_ context.Context, _ *pgxpool.Pool, key, _, _ string) (int, error) {
	f.calls = append(f.calls, key)
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
