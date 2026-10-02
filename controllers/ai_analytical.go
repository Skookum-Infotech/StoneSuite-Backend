package controllers

import (
	"regexp"
	"sort"
	"strings"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

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
	"exist": true, "were": true, "was": true,
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
var countWordRe = regexp.MustCompile(`[a-z_]+`)

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
	question = normalizeModuleNouns(question)
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

// countTypeForWord maps a normalized word to the record type it counts: a CRM
// type word or an AI-indexed module noun.
func countTypeForWord(w string) (string, bool) {
	if key, ok := countCRMTypeKeys[w]; ok {
		return key, true
	}
	return moduleTypeForWord(w)
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
		if key, ok := countTypeForWord(w); ok {
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
			if countFillerWords[w] || filterHintSet[w] {
				// Harmless trailing word ("do I have", "in total", "are
				// there", "exist") — keep scanning, doesn't change the
				// count. A filter-hint word ("closed", "won", "last") is
				// left to classifyCountQuestion's filterHintRe check on the
				// whole question, so hasFilterHintCountIntent still sees
				// this as "found an object" and can trigger the routed path.
				continue
			}
			// A meaningful qualifier follows the type word(s) ("customers in
			// Texas", "leads does John have") — this clause narrows the
			// count in a way this path cannot safely honor. Bail out of the
			// WHOLE question rather than silently answering an unfiltered
			// total mislabeled as the answer to a filtered question.
			return nil, false
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

// countFollowUpRe matches a short follow-up that names only a new CRM type
// after a previous count question — "and customers?", "what about
// prospects", "and leads".
var countFollowUpRe = regexp.MustCompile(`(?i)^\s*(?:and(?: what about)?|what about)\s+([a-z_ ]+?)\s*\??\s*$`)

// classifyFollowUpCount reports whether question is a short follow-up naming
// only a new CRM type ("and customers?", "what about prospects") continuing a
// previous count question in history, and if so which type to count.
// Deliberately does not re-run filterHintRe against question itself — the
// filter-hint guard already applies to the ORIGINAL count question this
// follow-up continues from (countObject(prev)); a bare "and <type>?" carries
// no filter language of its own to guard against.
func classifyFollowUpCount(question string, history []ragcore.Message) ([]string, bool) {
	m := countFollowUpRe.FindStringSubmatch(question)
	if m == nil {
		return nil, false
	}
	m[1] = normalizeModuleNouns(m[1])
	prev := previousUserQuestion(history)
	if prev == "" {
		return nil, false
	}
	if _, ok := countObject(prev); !ok {
		return nil, false
	}
	key, ok := countTypeForWord(strings.ToLower(m[1]))
	if !ok {
		return nil, false
	}
	return []string{key}, true
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

// openClosedHint is the exact set of filter-hint words classifyOpenClosedCount
// resolves deterministically against a workflow's own IsTerminal flag per
// status, rather than asking the LLM router to guess a literal status name:
// true means "open" (not yet terminal), false means "closed" (terminal).
// Deliberately small and literal — words like "pending"/"dead"/"stalled" stay
// out even though they're in filterHintWords, since they may or may not be an
// exact status name in a given tenant's workflow and this path only ever
// resolves the generic open/closed concept, never a specific status.
var openClosedHint = map[string]bool{
	"open":   true,
	"active": true,
	"closed": false,
}

// classifyOpenClosedCount reports whether question is a pure "how many open/
// closed <CRM type>" question answerable deterministically via each matched
// workflow's own terminal/non-terminal status set (see countCRMRecordsOpenClosed)
// — no LLM call, no guessing whether "open" matches any of the tenant's actual
// status labels. Requires every filter-hint word present to be one of
// openClosedHint's and all of one polarity; a question combining "open" with
// any other filter word (a date, a specific status, "open" and "closed"
// together) is left to resolveRoutedFilteredCount/RAG — this path only ever
// applies a single open/closed predicate, never combines filters, matching
// this package's existing bail-rather-than-guess philosophy. Module types
// (invoice, sales_order, ...) are left to moduleFilteredCountNote's existing
// "can't filter that yet" path — they don't share workflow.StatusInfo's
// IsTerminal concept.
func classifyOpenClosedCount(question string) (keys []string, open bool, ok bool) {
	keys, matched := countObject(question)
	if !matched || anyModuleType(keys) {
		return nil, false, false
	}
	hints := filterHintRe.FindAllString(strings.ToLower(question), -1)
	if len(hints) == 0 {
		return nil, false, false
	}
	var want *bool
	for _, h := range hints {
		v, known := openClosedHint[h]
		if !known {
			return nil, false, false
		}
		if want != nil && *want != v {
			return nil, false, false // mixed "open" and "closed" — ambiguous
		}
		want = &v
	}
	return keys, *want, true
}

// sumIntentRe matches phrasing asking for an aggregate dollar total ("total
// outstanding balance", "how much is outstanding", "sum of balances") — the
// SUM counterpart to countIntentRe: that regex (and countObject downstream of
// it) only ever recognizes a question counting RECORDS, never one aggregating
// a FIELD across them, so a sum-shaped question needs its own classifier
// rather than reusing countObject's word-scan.
var sumIntentRe = regexp.MustCompile(`(?i)\b(?:total|sum|how much)\b`)

// sumFieldWordRe matches the money-field words this path knows how to sum —
// "balance"/"outstanding"/"owed"/"due" for a module's own outstanding-balance
// field. Deliberately narrow: a "total" question about some other field
// (grand_total, tax_total, a different module's own numeric field) is out of
// scope for this pass and falls through to RAG rather than guessing which
// field the question means.
var sumFieldWordRe = regexp.MustCompile(`(?i)\b(?:balances?|outstanding|owe[ds]?|due)\b`)

// questionNamesModule reports whether question names the AI-indexed module
// wantType by noun (singular or plural, e.g. "invoice"/"invoices") — reusing
// moduleLex/moduleTypeForWord (see ai_modules.go) rather than a new regex.
func questionNamesModule(question, wantType string) bool {
	norm := normalizeModuleNouns(question)
	for _, w := range countWordRe.FindAllString(norm, -1) {
		if t, ok := moduleTypeForWord(w); ok && t == wantType {
			return true
		}
	}
	return false
}

// classifySumQuestion reports whether question is a pure "total/how much
// outstanding balance" question this path can answer deterministically via
// the invoice module's own Sum AI hook (a real SQL SUM — see
// globalsearch.AISumFunc) — never an LLM asked to add up whatever a low-K RAG
// retrieval happened to surface, which is what silently produced wrong totals
// before this path existed. Deliberately narrow and invoice-only for this
// pass (other modules can get a Sum hook later via the same pattern): any
// filter-hint word present (a date range, a status) makes this a FILTERED
// sum, which this path doesn't support, so it falls through to RAG rather
// than silently answering an unfiltered total mislabeled as a filtered one —
// the same bail-rather-than-guess rule classifyCountQuestion already follows.
func classifySumQuestion(question string) (key string, ok bool) {
	if !sumIntentRe.MatchString(question) || !sumFieldWordRe.MatchString(question) {
		return "", false
	}
	if filterHintRe.MatchString(question) {
		return "", false
	}
	if !questionNamesModule(question, "invoice") {
		return "", false
	}
	return "invoice", true
}
