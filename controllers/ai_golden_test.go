package controllers

import "testing"

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
