package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Skookum-Infotech/go-rag/route"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/crmstore"
	"stonesuite-backend/query"
)

// countCRMTypeKeys maps a keyword found in a question to the CRM workflow
// key it refers to. "crm"/"record"/"records" (with no more specific type
// word present) means "every type" — see classifyCountQuestion.
var countCRMTypeKeys = map[string]string{
	"lead": "lead", "leads": "lead",
	"prospect": "prospect", "prospects": "prospect",
	"customer": "customer", "customers": "customer",
}

// countIntentRe matches the narrow set of phrasings this path answers
// deterministically: "how many", "count of", "number of", "total" questions.
// Anything else — including any date/status/filter language — falls through
// to the existing RAG path unchanged; this path never guesses.
var countIntentRe = regexp.MustCompile(`(?i)\bhow many\b|\bcount of\b|\bnumber of\b|\btotal\b`)

// filterHintWords are words that signal the question wants a FILTERED count
// (by date, status, outcome, etc.), not a plain total. This task only
// implements pure counts (see package doc / task scope) — there is no
// reliable "won_at"/"closed_at" timestamp on the customer table to honor a
// date filter, and guessing a status filter is just as unsafe. A question
// like "how many customers won last week" DOES match countIntentRe + a CRM
// type word, but answering it with an unfiltered total would silently
// mislabel that total as the answer to the filtered question — a
// "confidently wrong" bug, strictly worse than RAG's honest "I don't have
// that information". So: any filter-hint word present forces fallthrough to
// RAG, full stop, even though it means an honest non-answer today.
var filterHintWords = []string{
	"last", "next", "this week", "week", "month", "year", "yesterday", "today",
	"won", "lost", "closed", "since", "between", "before", "after", "quarter",
	"qualified", "unqualified", "stage", "pipeline", "funnel", "converted",
	"open", "pending", "active", "inactive", "dead", "stalled", "approved",
	"rejected", "renewal",
}

// classifyCountQuestion reports whether question is a pure count-of-CRM-type
// question this path can answer deterministically, and which workflow key(s)
// to count. It deliberately does NOT attempt to parse dates, statuses, or any
// other filter out of the question — a question mentioning a filter concept
// (e.g. "last week", "won", "closed") must NOT match, since this path has no
// safe way to honor that filter; it falls through to RAG instead, which
// correctly says "I don't have that information" rather than this path
// silently returning an unfiltered total mislabeled as a filtered one.
func classifyCountQuestion(question string) (keys []string, ok bool) {
	if !countIntentRe.MatchString(question) {
		return nil, false
	}
	lower := strings.ToLower(question)
	for _, hint := range filterHintWords {
		if strings.Contains(lower, hint) {
			return nil, false
		}
	}

	seen := map[string]bool{}
	var matchedTypeWord bool
	for word, key := range countCRMTypeKeys {
		if strings.Contains(lower, word) {
			matchedTypeWord = true
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
		}
	}
	if matchedTypeWord {
		sort.Strings(keys) // deterministic order regardless of map iteration
		return keys, true
	}
	// No specific type word — "how many CRM records/records do we have" means
	// every type, but ONLY if the question is otherwise about records at all.
	if strings.Contains(lower, "record") || strings.Contains(lower, "crm") {
		return crmstore.CRMWorkflowKeys(), true
	}
	return nil, false
}

// hasFilterHintCountIntent reports whether question is exactly the case
// classifyCountQuestion refuses: it matches countIntentRe, names a CRM
// type/record word, AND contains a filter-hint word. This is the narrow
// trigger for attempting LLM-routed counting (resolveRoutedFilteredCount) —
// checked separately from classifyCountQuestion so an unrelated "how many
// angels dance on a pin" question never pays for a routing call it has no
// chance of using.
func hasFilterHintCountIntent(question string) bool {
	if !countIntentRe.MatchString(question) {
		return false
	}
	lower := strings.ToLower(question)
	hasHint := false
	for _, hint := range filterHintWords {
		if strings.Contains(lower, hint) {
			hasHint = true
			break
		}
	}
	if !hasHint {
		return false
	}
	for word := range countCRMTypeKeys {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return strings.Contains(lower, "record") || strings.Contains(lower, "crm")
}

// routeFieldWhitelist is the fixed vocabulary the LLM router may name in a
// Filter.Field — deliberately smaller than a workflow's full FieldResolver
// surface. owner_user_id/team_id/id are scope's job, not the model's (see
// route's package doc); custom fields are left out because they vary per
// tenant and per workflow, and folding a tenant's whole custom-field schema
// into the routing prompt is a bigger feature than this pass justifies. These
// four system fields cover the filters Phase 3 exists to unblock ("closed
// last quarter", "qualified leads").
var routeFieldWhitelist = []string{"status", "created_at", "updated_at", "record_number"}

var routeFieldWhitelistSet = func() map[string]bool {
	m := make(map[string]bool, len(routeFieldWhitelist))
	for _, f := range routeFieldWhitelist {
		m[f] = true
	}
	return m
}()

// routeOperatorSet bounds Filter.Op to comparisons that take a single scalar
// value (route.Filter.Value is always a string) — OpIn/OpBetween need
// multi-value input the router's schema doesn't offer, so they're rejected
// here rather than mis-coerced.
var routeOperatorSet = map[query.Operator]bool{
	query.OpEq: true, query.OpNeq: true, query.OpGt: true, query.OpGte: true,
	query.OpLt: true, query.OpLte: true, query.OpContains: true, query.OpStartsWith: true,
	query.OpIsNull: true, query.OpIsEmpty: true,
}

// resolveRoutedFilteredCount attempts the LLM-routed filtered-count path: ask
// llm to classify question via route.Extract, and — only when the model
// returns intent=count with every filter inside routeFieldWhitelist/
// routeOperatorSet and every workflow key a real CRM key — execute a
// scope-composed, filtered count. Returns ok=false for every other outcome
// (routing unsupported, malformed model output, a non-count intent, or a
// filter/key outside the whitelist), which the caller treats as "fall back to
// plain retrieval" — never a fatal error, per the architecture plan's rule
// that adversarial/malformed model output must degrade, not break, the ask.
//
// Security: scope and actorIdentityID are the caller's, resolved from request
// context before this function is ever reached (see AIOps.Ask) — nothing
// route.Extract returns can influence WHOSE data is counted, only WHAT is
// counted. That is what routeFieldWhitelist/routeOperatorSet exist to keep
// true even if a record's content tries to steer the model via indirect
// prompt injection: the model can at most choose a bad-but-still-whitelisted
// filter, never a scope or identity value.
//
// history is the caller's prior conversation turns, if any (see
// ai.ConversationStore.History) — passed straight to route.Extract so a
// follow-up like "what about last month?" can resolve against the earlier
// turn's context. Same untrusted-input caveat as any other conversation
// content reaching the model: it can steer word choice, never scope.
func resolveRoutedFilteredCount(ctx context.Context, llm ragcore.LLMClient, store crmstore.Store, pool *pgxpool.Pool, scope, actorIdentityID, question string, history []ragcore.Message) (ragcore.AskResult, bool) {
	keys := crmstore.CRMWorkflowKeys()
	r, err := route.Extract(ctx, llm, keys, routeFieldWhitelist, question, history)
	if err != nil {
		slog.Warn("ai query routing unavailable; falling back to retrieval", "err", err)
		return ragcore.AskResult{}, false
	}
	if r.Intent != route.IntentCount {
		return ragcore.AskResult{}, false
	}

	matchedKeys := r.WorkflowKeys
	if len(matchedKeys) == 0 {
		matchedKeys = keys
	}
	validKeys := make(map[string]bool, len(keys))
	for _, k := range keys {
		validKeys[k] = true
	}
	for _, k := range matchedKeys {
		if !validKeys[k] {
			slog.Warn("ai query router named an unknown workflow key; falling back to retrieval", "key", k)
			return ragcore.AskResult{}, false
		}
	}

	clauses := make([]query.Clause, 0, len(r.Filters))
	for _, f := range r.Filters {
		if !routeFieldWhitelistSet[f.Field] {
			slog.Warn("ai query router named a field outside the routing whitelist; falling back to retrieval", "field", f.Field)
			return ragcore.AskResult{}, false
		}
		op := query.Operator(f.Op)
		if !routeOperatorSet[op] {
			slog.Warn("ai query router named an operator outside the routing whitelist; falling back to retrieval", "op", f.Op)
			return ragcore.AskResult{}, false
		}
		clauses = append(clauses, query.Clause{Field: f.Field, Op: op, Value: f.Value})
	}

	res, err := countCRMRecordsFiltered(ctx, store, pool, scope, actorIdentityID, matchedKeys, clauses)
	if err != nil {
		slog.Warn("ai routed count failed; falling back to retrieval", "err", err)
		return ragcore.AskResult{}, false
	}
	return res, true
}

// countCRMRecordsFiltered is CountRecordsFiltered summed across keys — the
// filtered counterpart to countCRMRecords, for the LLM-routed count path.
func countCRMRecordsFiltered(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, scope, actorIdentityID string, keys []string, filters []query.Clause) (ragcore.AskResult, error) {
	counts := make(map[string]int, len(keys))
	total := 0
	for _, key := range keys {
		n, err := store.CountRecordsFiltered(ctx, pool, key, scope, actorIdentityID, filters)
		if err != nil {
			return ragcore.AskResult{}, fmt.Errorf("count filtered %s records: %w", key, err)
		}
		counts[key] = n
		total += n
	}
	return ragcore.AskResult{Answer: formatCountAnswer(keys, counts, total), Citations: []ragcore.Citation{}}, nil
}

// countCRMRecords sums CountRecords across keys under one scope/identity,
// building a deterministic answer with zero LLM calls — a plain count needs
// no generation, and skipping the chat model avoids both its latency and any
// chance of it mis-stating the number. Citations are always an empty (never
// nil) slice, matching ragcore.AskResult's existing JSON convention.
func countCRMRecords(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, scope, actorIdentityID string, keys []string) (ragcore.AskResult, error) {
	counts := make(map[string]int, len(keys))
	total := 0
	for _, key := range keys {
		n, err := store.CountRecords(ctx, pool, key, scope, actorIdentityID)
		if err != nil {
			return ragcore.AskResult{}, fmt.Errorf("count %s records: %w", key, err)
		}
		counts[key] = n
		total += n
	}
	return ragcore.AskResult{Answer: formatCountAnswer(keys, counts, total), Citations: []ragcore.Citation{}}, nil
}

// formatCountAnswer renders counts as a plain sentence. A single key renders
// as "You have N <key>s." (pluralizing the CRM type word); multiple keys
// (the "how many CRM records" case) list each type plus a total.
func formatCountAnswer(keys []string, counts map[string]int, total int) string {
	if len(keys) == 1 {
		return fmt.Sprintf("You have %d %s.", counts[keys[0]], pluralize(keys[0], counts[keys[0]]))
	}
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", counts[key], pluralize(key, counts[key])))
	}
	return fmt.Sprintf("You have %s (%d CRM records total).", strings.Join(parts, ", "), total)
}

// pluralize returns key ("lead"/"prospect"/"customer") pluralized for n.
func pluralize(key string, n int) string {
	if n == 1 {
		return key
	}
	return key + "s"
}
