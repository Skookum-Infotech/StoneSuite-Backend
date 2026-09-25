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
var countFollowUpRe = regexp.MustCompile(`(?i)^\s*(?:and(?: what about)?|what about)\s+(lead|leads|prospect|prospects|customer|customers)\s*\??\s*$`)

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
	prev := previousUserQuestion(history)
	if prev == "" {
		return nil, false
	}
	if _, ok := countObject(prev); !ok {
		return nil, false
	}
	key, ok := countCRMTypeKeys[strings.ToLower(m[1])]
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
