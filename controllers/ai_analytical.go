package controllers

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/ai"
	"stonesuite-backend/crmstore"
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

// countCRMRecords sums CountRecords across keys under one scope/identity,
// building a deterministic answer with zero LLM calls — a plain count needs
// no generation, and skipping the chat model avoids both its latency and any
// chance of it mis-stating the number. Citations are always an empty (never
// nil) slice, matching ai.AskResult's existing JSON convention.
func countCRMRecords(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, scope, actorIdentityID string, keys []string) (ai.AskResult, error) {
	counts := make(map[string]int, len(keys))
	total := 0
	for _, key := range keys {
		n, err := store.CountRecords(ctx, pool, key, scope, actorIdentityID)
		if err != nil {
			return ai.AskResult{}, fmt.Errorf("count %s records: %w", key, err)
		}
		counts[key] = n
		total += n
	}
	return ai.AskResult{Answer: formatCountAnswer(keys, counts, total), Citations: []ai.Citation{}}, nil
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
