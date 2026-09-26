// Package route provides cheap, schema-constrained query routing: one LLM
// call that classifies a question as a plain count, a filtered search, or a
// single-record lookup, and — for count/lookup — extracts which caller-
// supplied record types and filters it's asking about.
//
// This exists as an alternative to a tool-calling agent loop, which needs a
// much larger model (unreliable tool-calling below ~7B) and several
// sequential generations per answer. Schema-constrained extraction works
// reliably on a small model because the decoder is constitutionally unable to
// emit anything but the shape a Route needs — see rag.StructuredLLMClient.
//
// Security contract, non-negotiable: a Route never carries identity, tenant,
// or scope. The model chooses WHAT to look for (record types, filter fields
// visible to it, free-text search terms); the caller alone decides WHOSE data
// that search may touch, by construction — the same rule rag.Corpus already
// enforces for retrieval. Callers must additionally validate every Filter's
// Field against their own whitelist before turning it into a real query: this
// package validates shape (is Intent one of the three known values) but has
// no notion of which fields are safe to filter on for a given caller.
package route

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Skookum-Infotech/go-rag/rag"
)

// Intent is what kind of answer a Route asks for.
type Intent string

const (
	// IntentCount asks for a (possibly filtered) count of matching records —
	// no generation needed once Filters are resolved to a real query.
	IntentCount Intent = "count"
	// IntentSearch asks a free-text question best answered by retrieval +
	// synthesis — the caller's existing RAG path, unchanged.
	IntentSearch Intent = "search"
	// IntentLookup asks about one specific record.
	IntentLookup Intent = "lookup"
)

// validIntents is the fixed set Extract will accept from the model. Anything
// else is malformed output, not a fourth intent to plumb through.
var validIntents = map[Intent]bool{IntentCount: true, IntentSearch: true, IntentLookup: true}

// Filter is one generic field comparison the model extracted from the
// question. Field and Op are the model's own vocabulary — the caller resolves
// Field against its own whitelist and Op against its own operator set (e.g.
// StoneSuite's query.FieldResolver / query.Operator) before trusting either.
// Value is always a string so the JSON Schema stays simple enough for a small
// model to fill reliably; callers coerce it (date parse, etc.) as needed.
type Filter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

// Route is the model's classification of one question.
type Route struct {
	Intent       Intent   `json:"intent"`
	WorkflowKeys []string `json:"workflow_keys"`
	Filters      []Filter `json:"filters"`
	SearchText   string   `json:"search_text"`
	// NoFilter is computed by Extract, never set by the model: true when
	// Intent needs WorkflowKeys/Filters to narrow a real query (count,
	// lookup) but the model returned neither — it failed to extract anything
	// to narrow the question, not that it deliberately chose "every record".
	// Always false for IntentSearch, where empty WorkflowKeys/Filters is
	// normal (search narrows via SearchText, not filters).
	//
	// Callers MUST check this before turning a Route into a real query:
	// silently running a count/lookup with zero filters reads as a
	// successful unfiltered result, not the routing failure it actually was.
	// Existing callers that don't check it keep exactly the behavior they
	// always had — this is a new, purely additive field.
	NoFilter bool `json:"no_filter"`
}

// ErrUnsupported is returned by Extract when llm does not implement
// rag.StructuredLLMClient. Routing depends on server-side schema constraints
// to be reliable on a small model — without them, callers should treat
// routing as unavailable and fall back to their own default path (typically
// plain retrieval), not treat this as a fatal error.
var ErrUnsupported = errors.New("route: llm does not support structured output (rag.StructuredLLMClient)")

// schema is the fixed JSON Schema every Extract call constrains output to.
// workflowKeys and fieldHints are folded into the system prompt text, not the
// schema, so the schema itself never needs to change per caller.
var schema = []byte(`{
	"type": "object",
	"properties": {
		"intent": {"type": "string", "enum": ["count", "search", "lookup"]},
		"workflow_keys": {"type": "array", "items": {"type": "string"}},
		"filters": {
			"type": "array",
			"items": {
				"type": "object",
				"properties": {
					"field": {"type": "string"},
					"op": {"type": "string"},
					"value": {"type": "string"}
				},
				"required": ["field", "op", "value"]
			}
		},
		"search_text": {"type": "string"}
	},
	"required": ["intent", "workflow_keys", "filters", "search_text"]
}`)

// options carries Extract's optional behavior — see WithToday and
// WithAllowedValues. Zero value reproduces Extract's original prompt
// byte-for-byte, which is what keeps existing callers (that pass no Option)
// compiling and behaving unchanged.
type options struct {
	today         time.Time
	allowedValues map[string][]string
}

// Option configures one optional piece of Extract's behavior. Variadic and
// additive by design: existing Extract call sites need no changes.
type Option func(*options)

// WithToday tells the model today's date (as YYYY-MM-DD) so relative dates in
// the question — "this month", "last week" — resolve against a concrete day
// instead of whatever stale or absent notion of "now" the model would
// otherwise guess at. Omitted (zero time.Time) leaves the prompt exactly as
// it was before this option existed.
func WithToday(today time.Time) Option {
	return func(o *options) { o.today = today }
}

// WithAllowedValues lists, per field name (e.g. "status"), the valid values
// Extract's model may use in an extracted Filter for that field. The prompt
// instructs the model to use only a listed value or omit the filter
// entirely — never invent one outside the caller's own vocabulary. Fields not
// present in the map are unconstrained (matching Extract's behavior without
// this option).
func WithAllowedValues(allowed map[string][]string) Option {
	return func(o *options) { o.allowedValues = allowed }
}

// buildSystemPrompt tells the model its vocabulary: which record types
// (workflowKeys) and which filter fields (fieldHints) it may name, plus
// today's date and any per-field allowed values from opts. It is
// deliberately silent about identity, tenant, or scope — those are not the
// model's concern, see the package doc.
func buildSystemPrompt(workflowKeys, fieldHints []string, cfg options) string {
	var b strings.Builder
	fmt.Fprintf(&b, `Classify the user's question about their own CRM data.

intent is exactly one of:
- "count": the question asks how many records match some criteria.
- "search": the question asks about record content in prose (needs a written answer).
- "lookup": the question asks about one specific named record.

workflow_keys names which of these record types the question is about (use
all of them if unclear, none if not applicable): %s.

filters is a list of {field, op, value} triples narrowing a count or lookup.
Only use field values from: %s. op is one of: eq, neq, gt, gte, lt, lte,
contains, startswith, is_null, is_empty. Leave filters empty for a plain,
unfiltered question. Never invent a field not in that list.

search_text is the question rewritten as a plain search query, used only when
intent is "search" or "lookup".`,
		strings.Join(workflowKeys, ", "), strings.Join(fieldHints, ", "))

	if !cfg.today.IsZero() {
		fmt.Fprintf(&b, "\n\nToday's date is %s. Resolve relative dates in the question (e.g. \"this month\", \"last week\") against this date.", cfg.today.Format("2006-01-02"))
	}

	if len(cfg.allowedValues) > 0 {
		fields := make([]string, 0, len(cfg.allowedValues))
		for field := range cfg.allowedValues {
			fields = append(fields, field)
		}
		sort.Strings(fields) // deterministic prompt text across calls
		b.WriteString("\n\nFor these fields, the ONLY valid values are:")
		for _, field := range fields {
			fmt.Fprintf(&b, "\n- %s: %s", field, strings.Join(cfg.allowedValues[field], ", "))
		}
		b.WriteString("\nIf the question implies a value not in this list for one of these fields, omit that filter rather than inventing a value.")
	}

	b.WriteString("\n\nRespond with JSON matching the required schema only — no prose.")
	return b.String()
}

// Extract classifies question into a Route. workflowKeys and fieldHints bound
// the model's vocabulary for WorkflowKeys and Filter.Field respectively —
// callers should pass only values they are prepared to accept back (Extract
// itself does not enforce them; see the package doc on validating Filters).
// history is prior turns for a multi-turn conversation; pass nil for a
// single-turn ask. opts is optional — see WithToday and WithAllowedValues.
//
// Returns ErrUnsupported if llm cannot do schema-constrained output. Returns a
// wrapped error if the model's output isn't valid JSON or names an intent
// outside {count, search, lookup} — both signal "routing failed for this
// question", which callers should treat as a fallback trigger, not fatal.
//
// The returned Route's NoFilter is true when the model returned neither
// workflow_keys nor filters for an intent that needs them (count, lookup) —
// see Route.NoFilter. Callers must check it before treating an unfiltered
// count/lookup as a real answer rather than a failed extraction.
func Extract(ctx context.Context, llm rag.LLMClient, workflowKeys, fieldHints []string, question string, history []rag.Message, opts ...Option) (Route, error) {
	structured, ok := llm.(rag.StructuredLLMClient)
	if !ok {
		return Route{}, ErrUnsupported
	}

	var cfg options
	for _, opt := range opts {
		opt(&cfg)
	}

	system := buildSystemPrompt(workflowKeys, fieldHints, cfg)
	messages := make([]rag.Message, 0, len(history)+1)
	messages = append(messages, history...)
	messages = append(messages, rag.Message{Role: "user", Content: question})

	raw, err := structured.ChatJSON(ctx, system, messages, schema)
	if err != nil {
		return Route{}, fmt.Errorf("route: chat: %w", err)
	}

	var r Route
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Route{}, fmt.Errorf("route: malformed model output: %w", err)
	}
	if !validIntents[r.Intent] {
		return Route{}, fmt.Errorf("route: unknown intent %q", r.Intent)
	}
	r.NoFilter = r.Intent != IntentSearch && len(r.WorkflowKeys) == 0 && len(r.Filters) == 0
	return r, nil
}
