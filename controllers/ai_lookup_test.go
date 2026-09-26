package controllers

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

func TestExtractRecordNumber(t *testing.T) {
	tests := []struct {
		name     string
		question string
		want     string
		wantOK   bool
	}{
		{"simple lead number", "What's LEAD-000012's phone number?", "LEAD-000012", true},
		{"short digit run below threshold", "room B-12 is free", "", false},
		{"lowercase does not match", "lead-000012 phone", "", false},
		{"prospect number mid-sentence", "tell me about PROS-042 status", "PROS-042", true},
		{"no record number at all", "how many leads do we have", "", false},
		{"invoice-style prefix stops at the second hyphen", "what's the email on INV-2024-001", "INV-2024", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := extractRecordNumber(tt.question)
			if ok != tt.wantOK || got != tt.want {
				t.Errorf("extractRecordNumber(%q) = (%q, %v), want (%q, %v)", tt.question, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestLookupField(t *testing.T) {
	tests := []struct {
		name      string
		question  string
		wantAlias string
		wantKey   string
		wantOK    bool
	}{
		{"city", "What city is LEAD-000012 in?", "city", "customer_addr_city", true},
		{"phone", "What's the phone number on LEAD-1?", "phone", "customer_primary_phonenum", true},
		{"email", "What's the email for CUST-9?", "email", "customer_contact_email", true},
		{"address", "What's the address on PROS-4?", "address", "customer_addr_line1", true},
		{"status", "What's the status of LEAD-2?", "status", "crm_status_name", true},
		{"company", "What company is CUST-1?", "company", "customer_name", true},
		{"name", "What's the name on LEAD-3?", "name", "customer_name", true},
		{"no field word", "Tell me about LEAD-000012", "", "", false},
		{"owner not aliased", "who is the owner of LEAD-5", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alias, key, ok := lookupField(tt.question)
			if ok != tt.wantOK || alias != tt.wantAlias || key != tt.wantKey {
				t.Errorf("lookupField(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.question, alias, key, ok, tt.wantAlias, tt.wantKey, tt.wantOK)
			}
		})
	}
}

func TestLookupValueText(t *testing.T) {
	tests := []struct {
		name     string
		v        any
		wantText string
		wantOK   bool
	}{
		{"nil", nil, "", false},
		{"blank string", "   ", "   ", false},
		{"real string", "Bellari", "Bellari", true},
		{"int", 42, "42", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, ok := lookupValueText(tt.v)
			if ok != tt.wantOK || text != tt.wantText {
				t.Errorf("lookupValueText(%v) = (%q, %v), want (%q, %v)", tt.v, text, ok, tt.wantText, tt.wantOK)
			}
		})
	}
}

// fakeLookupStore is a minimal crmstore.Store test double for
// lookupRecordAnswer/findRecordByNumber: SearchRecords returns a canned page
// per workflow key, recording which keys (and in what order) it was asked to
// search — the property that proves the fast path never bypasses per-type
// scope.
type fakeLookupStore struct {
	fakeCountStore
	pages       map[string]workflow.Page
	searchCalls []string
}

func (f *fakeLookupStore) SearchRecords(_ context.Context, _ *pgxpool.Pool, key, _, _ string, _ query.Request) (workflow.Page, error) {
	f.searchCalls = append(f.searchCalls, key)
	return f.pages[key], nil
}

func TestLookupRecordAnswer(t *testing.T) {
	rec := workflow.Record{ID: "rec-1", CoreFields: map[string]any{"customer_addr_city": "Bellari"}}

	t.Run("no record number in question: not this path", func(t *testing.T) {
		store := &fakeLookupStore{}
		res, ok := lookupRecordAnswer(context.Background(), store, nil, ownGrants, "identity-1", "how many leads do we have")
		if ok {
			t.Fatalf("expected matched=false, got %+v", res)
		}
	})

	t.Run("record number with no recognized field: falls through to RAG", func(t *testing.T) {
		store := &fakeLookupStore{}
		_, ok := lookupRecordAnswer(context.Background(), store, nil, ownGrants, "identity-1", "tell me about LEAD-000012")
		if ok {
			t.Fatal("expected matched=false when no field alias is named")
		}
		if len(store.searchCalls) != 0 {
			t.Fatalf("must not search the store before a field is even recognized, got %v", store.searchCalls)
		}
	})

	t.Run("no grants: not-found answer without searching", func(t *testing.T) {
		store := &fakeLookupStore{}
		res, ok := lookupRecordAnswer(context.Background(), store, nil, ai.Grants{}, "identity-1", "what city is LEAD-000012 in")
		if !ok {
			t.Fatal("expected matched=true (this path owns the answer)")
		}
		if res.Answer != "I couldn't find a record numbered LEAD-000012 that you have access to." {
			t.Fatalf("Answer = %q", res.Answer)
		}
		if len(store.searchCalls) != 0 {
			t.Fatalf("must not search the store with no grants, got %v", store.searchCalls)
		}
	})

	t.Run("found: template answer with a record citation", func(t *testing.T) {
		store := &fakeLookupStore{pages: map[string]workflow.Page{"lead": {Records: []workflow.Record{rec}}}}
		res, ok := lookupRecordAnswer(context.Background(), store, nil, ai.Grants{"lead": ai.ScopeAll}, "identity-1", "what city is LEAD-000012 in")
		if !ok {
			t.Fatal("expected matched=true")
		}
		if res.Answer != "LEAD-000012's city is Bellari." {
			t.Fatalf("Answer = %q", res.Answer)
		}
		if len(res.Citations) != 1 || res.Citations[0].SourceID != "rec-1" || res.Citations[0].SourceType != ai.CorpusRecords {
			t.Fatalf("Citations = %+v", res.Citations)
		}
	})

	t.Run("not found under any granted type: not-found answer, never 'exists elsewhere'", func(t *testing.T) {
		store := &fakeLookupStore{pages: map[string]workflow.Page{}}
		res, ok := lookupRecordAnswer(context.Background(), store, nil, allGrants, "identity-1", "what city is LEAD-999999 in")
		if !ok {
			t.Fatal("expected matched=true")
		}
		if res.Answer != "I couldn't find a record numbered LEAD-999999 that you have access to." {
			t.Fatalf("Answer = %q", res.Answer)
		}
		// Every granted type is searched, scope-safe, before giving up.
		if len(store.searchCalls) != 3 {
			t.Fatalf("searchCalls = %v, want one call per granted type", store.searchCalls)
		}
	})

	t.Run("found but field is blank: says so, still cites the record", func(t *testing.T) {
		blank := workflow.Record{ID: "rec-2", CoreFields: map[string]any{"customer_addr_city": ""}}
		store := &fakeLookupStore{pages: map[string]workflow.Page{"lead": {Records: []workflow.Record{blank}}}}
		res, ok := lookupRecordAnswer(context.Background(), store, nil, ai.Grants{"lead": ai.ScopeAll}, "identity-1", "what city is LEAD-000012 in")
		if !ok {
			t.Fatal("expected matched=true")
		}
		if res.Answer != "LEAD-000012 doesn't have a city on file." {
			t.Fatalf("Answer = %q", res.Answer)
		}
		if len(res.Citations) != 1 || res.Citations[0].SourceID != "rec-2" {
			t.Fatalf("Citations = %+v", res.Citations)
		}
	})
}
