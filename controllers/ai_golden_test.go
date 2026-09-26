package controllers

import (
	"testing"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"
)

// goldenQuestions is the P2.2 golden-question regression set: realistic
// StoneSuite CRM questions, each labeled with the routing decision the AI
// assistant must make between the analytical count fast-path
// (classifyCountQuestion, see ai_analytical.go) and the RAG retrieval path
// (ai.Orchestrator.Ask). This is what turns "answer quality" for the
// count/retrieval boundary from vibes into a CI-enforced regression gate —
// a wrong routing decision here means either a user gets a wrong "confidently
// exact" number (routed to count when they wanted a filtered answer) or an
// unnecessary refusal (routed to RAG when an exact count was answerable).
//
// Scope note: this only covers the routing decision, which is pure and
// runs with no external dependencies (no DB, no Ollama) — so it's something
// `go test ./...` can gate in CI on every PR. Grading actual RAG answer
// quality (retrieval recall, citation correctness, refusal rate against a
// live model) needs a seeded test tenant and a running Ollama box, neither
// of which CI has today; see docs/ai-assistant.md's "Observability" section
// for the refusal-rate metric that substitutes for that in production.
var goldenQuestions = []struct {
	question string
	wantOK   bool
	wantKeys []string // only checked when wantOK
}{
	// --- Analytical: plain totals, must route to the exact count path ---
	{question: "How many leads do we have?", wantOK: true, wantKeys: []string{"lead"}},
	{question: "How many prospects are there?", wantOK: true, wantKeys: []string{"prospect"}},
	{question: "How many customers do we have?", wantOK: true, wantKeys: []string{"customer"}},
	{question: "What is the total number of leads?", wantOK: true, wantKeys: []string{"lead"}},
	{question: "Count of customers", wantOK: true, wantKeys: []string{"customer"}},
	{question: "number of prospects", wantOK: true, wantKeys: []string{"prospect"}},
	{question: "Total leads?", wantOK: true, wantKeys: []string{"lead"}},
	{question: "Give me the count of leads", wantOK: true, wantKeys: []string{"lead"}},
	{question: "How many total prospects", wantOK: true, wantKeys: []string{"prospect"}},
	{question: "How many leads and prospects do we have", wantOK: true, wantKeys: []string{"lead", "prospect"}},
	{question: "How many CRM records do we have in total?", wantOK: true, wantKeys: []string{"lead", "prospect", "customer"}},
	{question: "How many records exist?", wantOK: true, wantKeys: []string{"lead", "prospect", "customer"}},

	// --- Retrieval: meta/conceptual questions, no count intent at all ---
	{question: "What is a lead?", wantOK: false},
	{question: "What can the assistant do?", wantOK: false},
	{question: "Tell me about the CRM", wantOK: false},
	{question: "What is the average deal size?", wantOK: false},
	{question: "List all customers in the West region", wantOK: false},

	// --- Retrieval: specific-record questions, no count intent ---
	{question: "Tell me about lead INC-2023-Q4-011", wantOK: false},
	{question: "Who owns customer Acme Corp?", wantOK: false},
	{question: "What is the status of prospect XYZ?", wantOK: false},
	{question: "Who is assigned to this lead?", wantOK: false},
	{question: "What stage is this prospect in?", wantOK: false},

	// --- Retrieval: count intent + filter hint, MUST defer (critical guard) ---
	{question: "How many customers won last week?", wantOK: false},
	{question: "How many leads closed this month?", wantOK: false},
	{question: "How many prospects are pending?", wantOK: false},
	{question: "Total customers converted this quarter", wantOK: false},
	{question: "Number of leads created yesterday", wantOK: false},
	{question: "How many deals are open right now", wantOK: false},
	{question: "How many qualified leads do we have", wantOK: false},
	{question: "How many customers are in the renewal stage", wantOK: false},
	{question: "Total number of rejected prospects", wantOK: false},
	{question: "How many leads since last month", wantOK: false},

	// --- Retrieval: a CRM type word that is NOT what is being counted ---
	// (substring matching used to answer these with "You have N customers.")
	{question: "What's the total balance for customer Acme?", wantOK: false},
	{question: "How many emails did customer Acme send?", wantOK: false},
	{question: "How many leader boards are there?", wantOK: false},
	{question: "How many days until the lead converts", wantOK: false},

	// --- Analytical: fillers between the count phrase and the type ---
	{question: "How many of our leads do we have", wantOK: true, wantKeys: []string{"lead"}},
	{question: "how many customers or prospects are there", wantOK: true, wantKeys: []string{"customer", "prospect"}},
}

// TestGoldenQuestionSet runs every entry in goldenQuestions through the same
// classifier the live /api/tenant/ai/ask handler uses, and fails the build if
// any question's routing decision regresses.
func TestGoldenQuestionSet(t *testing.T) {
	for _, tt := range goldenQuestions {
		t.Run(tt.question, func(t *testing.T) {
			keys, ok := classifyCountQuestion(tt.question)
			if ok != tt.wantOK {
				t.Fatalf("classifyCountQuestion(%q) ok = %v, want %v (keys=%v)", tt.question, ok, tt.wantOK, keys)
			}
			if !ok {
				return
			}
			if len(keys) != len(tt.wantKeys) {
				t.Fatalf("classifyCountQuestion(%q) keys = %v, want %v", tt.question, keys, tt.wantKeys)
			}
			for i, k := range tt.wantKeys {
				if keys[i] != k {
					t.Fatalf("classifyCountQuestion(%q) keys = %v, want %v", tt.question, keys, tt.wantKeys)
				}
			}
		})
	}
}

// goldenRoutingCases is the golden-question set for the routing decisions
// runAskDispatch makes ahead of (or instead of) classifyCountQuestion:
// intent classification (ai.NewAssistant.ForCaller's corpus/prompt choice),
// the record-number lookup fast path, the qualifier fall-through fix
// (Phase 6a), and the count follow-up path (Phase 6d). Each row exercises
// exactly one pure classifier — no DB, no Ollama — same CI-gating rationale
// as goldenQuestions above.
var goldenRoutingCases = []struct {
	name string
	run  func(t *testing.T)
}{
	// --- intent: data ---
	{"data: record type word", func(t *testing.T) {
		if got := classifyIntent("Show me my leads", ""); got != intentData {
			t.Errorf("classifyIntent = %q, want data", got)
		}
	}},
	{"data: record number", func(t *testing.T) {
		if got := classifyIntent("What's the status of LEAD-000012?", ""); got != intentData {
			t.Errorf("classifyIntent = %q, want data", got)
		}
	}},
	// --- intent: help ---
	{"help: how do I", func(t *testing.T) {
		if got := classifyIntent("How do I set up SSO?", ""); got != intentHelp {
			t.Errorf("classifyIntent = %q, want help", got)
		}
	}},
	// --- intent: mixed (neither cue) ---
	{"mixed: no cue, no history", func(t *testing.T) {
		if got := classifyIntent("hello", ""); got != intentMixed {
			t.Errorf("classifyIntent = %q, want mixed", got)
		}
	}},
	// --- intent: follow-up inherits previous turn ---
	{"follow-up inherits data intent", func(t *testing.T) {
		if got := classifyIntent("and its city?", "What's LEAD-000012's phone number?"); got != intentData {
			t.Errorf("classifyIntent = %q, want data", got)
		}
	}},

	// --- lookup: record number + recognized field matches ---
	{"lookup: record number + field matches", func(t *testing.T) {
		number, hasField, ok := lookupTemplateMatch("What city is LEAD-000012 in?")
		if !ok || !hasField || number != "LEAD-000012" {
			t.Errorf("lookupTemplateMatch = (%q, %v, %v), want (LEAD-000012, true, true)", number, hasField, ok)
		}
	}},
	// --- lookup: record number with no recognized field falls through ---
	{"lookup: record number, no field -> falls through to RAG", func(t *testing.T) {
		_, hasField, ok := lookupTemplateMatch("Tell me about LEAD-000012")
		if !ok || hasField {
			t.Errorf("lookupTemplateMatch hasField = %v, want false (no field named)", hasField)
		}
	}},
	// --- lookup: no record number at all is not this path ---
	{"lookup: no record number -> not this path", func(t *testing.T) {
		if _, _, ok := lookupTemplateMatch("how many leads do we have"); ok {
			t.Error("lookupTemplateMatch matched a question with no record number")
		}
	}},

	// --- qualifier fall-through (Phase 6a): a meaningful word after the
	// type word must defer to RAG, not answer an unfiltered total ---
	{"qualifier: 'customers in Texas' falls through", func(t *testing.T) {
		if _, ok := classifyCountQuestion("how many customers in Texas"); ok {
			t.Error("expected fall-through for a qualifier naming a place")
		}
	}},
	{"qualifier: 'leads does John have' falls through", func(t *testing.T) {
		if _, ok := classifyCountQuestion("how many leads does John have"); ok {
			t.Error("expected fall-through for a qualifier naming a person")
		}
	}},

	// --- count follow-up (Phase 6d) ---
	{"follow-up: 'and customers?' after a count question", func(t *testing.T) {
		history := []ragcore.Message{{Role: ragcore.RoleUser, Content: "how many leads do we have"}}
		keys, ok := classifyFollowUpCount("and customers?", history)
		if !ok || len(keys) != 1 || keys[0] != "customer" {
			t.Errorf("classifyFollowUpCount = (%v, %v), want ([customer], true)", keys, ok)
		}
	}},
	{"follow-up: no prior count question -> not a follow-up", func(t *testing.T) {
		history := []ragcore.Message{{Role: ragcore.RoleUser, Content: "how do I export a report"}}
		if _, ok := classifyFollowUpCount("and customers?", history); ok {
			t.Error("expected no match: the previous question was not a count")
		}
	}},
}

// TestGoldenRoutingSet runs goldenRoutingCases — see its doc comment.
func TestGoldenRoutingSet(t *testing.T) {
	for _, tt := range goldenRoutingCases {
		t.Run(tt.name, tt.run)
	}
}
