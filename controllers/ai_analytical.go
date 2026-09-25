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

	"stonesuite-backend/ai"
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

// countIntentRe finds the count phrase whose OBJECT countObject then parses:
// "how many", "count of", "number of", "total". Anything else — including any
// date/status/filter language — falls through to the existing RAG path
// unchanged; this path never guesses.
var countIntentRe = regexp.MustCompile(`(?i)\b(?:how many|count of|number of|total|count)\b`)

// countFillerWords may sit between a count phrase and the CRM type it counts
// without changing what is being counted ("how many of our leads", "total
// number of CRM records").
var countFillerWords = map[string]bool{
	"my": true, "our": true, "the": true, "all": true, "of": true, "do": true, "does": true,
	"we": true, "i": true, "you": true, "have": true, "has": true, "are": true, "is": true,
	"there": true, "in": true, "crm": true, "number": true, "currently": true, "total": true,
}

// countObjectMaxWords bounds how far past the count phrase countObject looks.
const countObjectMaxWords = 6

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

// filterHintRe matches filterHintWords as whole words/phrases — a substring
// check let "opened" match "open" and "lastly" match "last".
var filterHintRe = func() *regexp.Regexp {
	quoted := make([]string, len(filterHintWords))
	for i, w := range filterHintWords {
		quoted[i] = regexp.QuoteMeta(w)
	}
	return regexp.MustCompile(`(?i)\b(?:` + strings.Join(quoted, "|") + `)\b`)
}()

// filterHintSet is filterHintWords as single words, for countObject's
// skip-over check ("how many qualified leads").
var filterHintSet = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range filterHintWords {
		for _, part := range strings.Fields(w) {
			m[part] = true
		}
	}
	return m
}()

// countWordRe splits a question into lowercase words for countObject.
var countWordRe = regexp.MustCompile(`[a-z]+`)

// countStrongPhraseRe marks the start of an explicit count clause. A question
// can hold several ("how many customers and how many leads"); the weaker
// "total"/"count" only ever open the first clause, so "how many customers in
// total" is still one clause.
var countStrongPhraseRe = regexp.MustCompile(`(?i)\b(?:how many|count of|number of)\b`)

// countObject reports which CRM type(s) a count question is counting — the
// OBJECT of each count phrase, not merely a type word somewhere in it. After a
// count phrase it skips filler and filter words, then collects type words
// joined by "and"/"or"; the first other word ends that object. So "how many
// qualified leads and customers" counts lead+customer, while "total balance
// for customer Acme" (object "balance") and "how many emails did customer X
// send" (object "emails") are not CRM counts at all. "records" with no type
// word means every type.
//
// Further explicit count phrases ("how many customers and how many leads")
// each contribute their own object, and every one must name a CRM type: a
// clause we can't answer ("... and how many emails") sends the whole question
// to RAG rather than answering only the half we understood.
func countObject(question string) ([]string, bool) {
	first := countIntentRe.FindStringIndex(question)
	if first == nil {
		return nil, false
	}
	starts := []int{first[1]}
	for _, loc := range countStrongPhraseRe.FindAllStringIndex(question, -1) {
		if loc[0] >= first[1] {
			starts = append(starts, loc[1])
		}
	}
	seen := map[string]bool{}
	var keys []string
	for i, from := range starts {
		to := len(question)
		if i+1 < len(starts) {
			to = nextClauseStart(question, starts[i+1])
		}
		text := question[from:to]
		if len(starts) > 1 && len(countWordRe.FindAllString(strings.ToLower(text), -1)) == 0 {
			continue
		}
		clauseKeys, ok := countClauseObject(text)
		if !ok {
			return nil, false
		}
		for _, k := range clauseKeys {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	if len(keys) == 0 {
		return nil, false
	}
	if len(starts) > 1 {
		sort.Strings(keys) // deterministic order regardless of phrasing
	}
	return keys, true
}

// nextClauseStart returns where the count phrase ending at phraseEnd begins, so
// the previous clause's text stops before it.
func nextClauseStart(question string, phraseEnd int) int {
	for _, loc := range countStrongPhraseRe.FindAllStringIndex(question, -1) {
		if loc[1] == phraseEnd {
			return loc[0]
		}
	}
	return phraseEnd
}

// countClauseObject parses the text after ONE count phrase into CRM type keys.
func countClauseObject(text string) ([]string, bool) {
	words := countWordRe.FindAllString(strings.ToLower(text), -1)
	if len(words) > countObjectMaxWords {
		words = words[:countObjectMaxWords]
	}
	seen := map[string]bool{}
	var keys []string
	all := false
	for _, w := range words {
		if key, ok := countCRMTypeKeys[w]; ok {
			if !seen[key] {
				seen[key] = true
				keys = append(keys, key)
			}
			continue
		}
		if w == "record" || w == "records" {
			all = true
			continue
		}
		if len(keys) > 0 || all {
			if w == "and" || w == "or" {
				continue
			}
			break
		}
		if countFillerWords[w] || filterHintSet[w] {
			continue
		}
		break
	}
	if len(keys) > 0 {
		sort.Strings(keys) // deterministic order regardless of phrasing
		return keys, true
	}
	if all {
		return crmstore.CRMWorkflowKeys(), true
	}
	return nil, false
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
	keys, ok = countObject(question)
	if !ok || filterHintRe.MatchString(question) {
		return nil, false
	}
	return keys, true
}

// hasFilterHintCountIntent reports whether question is exactly the case
// classifyCountQuestion refuses: it matches countIntentRe, names a CRM
// type/record word, AND contains a filter-hint word. This is the narrow
// trigger for attempting LLM-routed counting (resolveRoutedFilteredCount) —
// checked separately from classifyCountQuestion so an unrelated "how many
// angels dance on a pin" question never pays for a routing call it has no
// chance of using.
func hasFilterHintCountIntent(question string) bool {
	_, ok := countObject(question)
	return ok && filterHintRe.MatchString(question)
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
// Security: grants and actorIdentityID are the caller's, resolved from request
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
func resolveRoutedFilteredCount(ctx context.Context, llm ragcore.LLMClient, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID, question string, history []ragcore.Message) (ragcore.AskResult, bool) {
	if len(grants.Types()) == 0 {
		// Nothing countable: don't spend a model call deciding what to count.
		return ragcore.AskResult{}, false
	}
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

	res, err := countCRMRecordsFiltered(ctx, store, pool, grants, actorIdentityID, matchedKeys, clauses)
	if err != nil {
		slog.Warn("ai routed count failed; falling back to retrieval", "err", err)
		return ragcore.AskResult{}, false
	}
	return res, true
}

// countCRMRecordsFiltered is CountRecordsFiltered summed across keys — the
// filtered counterpart to countCRMRecords, for the LLM-routed count path.
func countCRMRecordsFiltered(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID string, keys []string, filters []query.Clause) (ragcore.AskResult, error) {
	return countGranted(grants, keys, func(key, scope string) (int, error) {
		n, err := store.CountRecordsFiltered(ctx, pool, key, scope, actorIdentityID, filters)
		if err != nil {
			return 0, fmt.Errorf("count filtered %s records: %w", key, err)
		}
		return n, nil
	})
}

// countCRMRecords sums CountRecords across keys, building a deterministic
// answer with zero LLM calls — a plain count needs no generation, and skipping
// the chat model avoids both its latency and any chance of it mis-stating the
// number. Citations are always an empty (never nil) slice, matching
// ragcore.AskResult's existing JSON convention.
func countCRMRecords(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID string, keys []string) (ragcore.AskResult, error) {
	return countGranted(grants, keys, func(key, scope string) (int, error) {
		n, err := store.CountRecords(ctx, pool, key, scope, actorIdentityID)
		if err != nil {
			return 0, fmt.Errorf("count %s records: %w", key, err)
		}
		return n, nil
	})
}

// countGranted counts each requested key the caller can read under THAT key's
// own scope — a customer:own grant must not narrow a lead:all count, nor a
// lead:all grant widen a customer count — and names the keys it left out
// instead of counting them. A question only about ungranted types gets the
// no-access sentence and no number at all.
func countGranted(grants ai.Grants, keys []string, count func(key, scope string) (int, error)) (ragcore.AskResult, error) {
	granted, denied := splitGranted(grants, keys)
	if len(granted) == 0 {
		return ragcore.AskResult{Answer: noAccessSentence(denied), Citations: []ragcore.Citation{}}, nil
	}
	counts := make(map[string]int, len(granted))
	total := 0
	for _, key := range granted {
		n, err := count(key, grants[key])
		if err != nil {
			return ragcore.AskResult{}, err
		}
		counts[key] = n
		total += n
	}
	answer := formatCountAnswer(granted, counts, total)
	if len(denied) > 0 {
		answer += " " + noAccessSentence(denied)
	}
	return ragcore.AskResult{Answer: answer, Citations: []ragcore.Citation{}}, nil
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
