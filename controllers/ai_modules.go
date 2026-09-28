package controllers

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/ai"
	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/globalsearch"
)

// moduleLexicon is what the question classifiers know about the AI-indexed
// modules: every noun a question may use (singular and "s" plural, multi-word
// nouns underscore-joined as "sales_order") mapped to its record type, plus the
// regexes that spot them. It is built once from globalsearch's AI registry,
// which is immutable after package init.
type moduleLexicon struct {
	nounToType map[string]string
	labels     map[string]string // record type -> singular display label
	nounRe     *regexp.Regexp    // any noun, raw question text (spaces, not underscores)
	multiRe    *regexp.Regexp    // multi-word nouns only, for normalization; nil if none
}

var moduleLex = sync.OnceValue(func() *moduleLexicon {
	lex := &moduleLexicon{nounToType: map[string]string{}, labels: map[string]string{}}
	var all, multi []string
	for _, p := range globalsearch.AIProviders() {
		lex.labels[p.AI.RecordType] = p.AI.Label
		for _, n := range p.AI.Nouns {
			n = strings.ToLower(strings.TrimSpace(n))
			for _, form := range []string{n, n + "s"} {
				lex.nounToType[strings.ReplaceAll(form, " ", "_")] = p.AI.RecordType
				all = append(all, regexp.QuoteMeta(form))
				if strings.Contains(form, " ") {
					multi = append(multi, regexp.QuoteMeta(form))
				}
			}
		}
	}
	// Longest first so "sales orders" wins over a shorter noun sharing a prefix.
	byLen := func(s []string) { sort.Slice(s, func(i, j int) bool { return len(s[i]) > len(s[j]) }) }
	byLen(all)
	byLen(multi)
	if len(all) > 0 {
		lex.nounRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join(all, "|") + `)\b`)
	}
	if len(multi) > 0 {
		lex.multiRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join(multi, "|") + `)\b`)
	}
	return lex
})

// normalizeModuleNouns lowercases s and joins each multi-word module noun with
// an underscore ("sales orders" -> "sales_orders") so word tokenizers see one
// token per noun.
func normalizeModuleNouns(s string) string {
	s = strings.ToLower(s)
	if re := moduleLex().multiRe; re != nil {
		s = re.ReplaceAllStringFunc(s, func(m string) string { return strings.ReplaceAll(m, " ", "_") })
	}
	return s
}

// moduleTypeForWord returns the module record type a normalized word names.
func moduleTypeForWord(w string) (string, bool) {
	t, ok := moduleLex().nounToType[w]
	return t, ok
}

// hasModuleNoun reports whether question names an AI-indexed module.
func hasModuleNoun(question string) bool {
	re := moduleLex().nounRe
	return re != nil && re.MatchString(question)
}

// isModuleType reports whether key is an AI-indexed module's record type (as
// opposed to a CRM workflow key).
func isModuleType(key string) bool {
	_, ok := moduleLex().labels[key]
	return ok
}

// recordTypeLabel is the singular display name of a record type: a module's
// Label, or the CRM key itself ("lead").
func recordTypeLabel(key string) string {
	if l, ok := moduleLex().labels[key]; ok {
		return l
	}
	return key
}

// anyModuleType reports whether keys include a module record type.
func anyModuleType(keys []string) bool {
	for _, k := range keys {
		if isModuleType(k) {
			return true
		}
	}
	return false
}

// countRecordType counts one granted record type under its own scope: a CRM key
// through the CRM store, a module through its AI Count hook.
func countRecordType(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, actorIdentityID, key, scope string) (int, error) {
	if !isModuleType(key) {
		n, err := store.CountRecords(ctx, pool, key, scope, actorIdentityID)
		if err != nil {
			return 0, fmt.Errorf("count %s records: %w", key, err)
		}
		return n, nil
	}
	p, ok := globalsearch.AIByRecordType(key)
	if !ok || p.AI.Count == nil {
		return 0, fmt.Errorf("count %s: no AI count hook", key)
	}
	n, err := p.AI.Count(ctx, pool, authz.Scope(scope), actorIdentityID)
	if err != nil {
		return 0, fmt.Errorf("count %s records: %w", key, err)
	}
	return n, nil
}

// moduleFilteredCountNote answers "how many <filter> <module>" honestly: the
// routed filtered count only understands CRM statuses, so for a module it says
// so and gives the unfiltered total, labelled as such — never a total passed
// off as the filtered figure.
func moduleFilteredCountNote(ctx context.Context, store crmstore.Store, pool *pgxpool.Pool, grants ai.Grants, actorIdentityID string, keys []string) (ragcore.AskResult, error) {
	res, err := countGranted(grants, keys, "", func(key, scope string) (int, error) {
		return countRecordType(ctx, store, pool, actorIdentityID, key, scope)
	})
	if err != nil {
		return res, err
	}
	if len(splitGrantedKeys(grants, keys)) > 0 {
		res.Answer += " I can't filter that count by status or date yet, so that is the total."
	}
	return res, nil
}

// splitGrantedKeys returns the keys grants can read.
func splitGrantedKeys(grants ai.Grants, keys []string) []string {
	granted, _ := splitGranted(grants, keys)
	return granted
}

// moduleNumberSearchCap is how many hits each module's number search reads; a
// record number is unique per module, so a handful is ample.
const moduleNumberSearchCap = 5

// findModuleRecordByNumber looks number up in every module the caller can read,
// through that module's own scoped Search (the same query its list endpoint
// runs, so it cannot reach a row outside the caller's grant), and returns the
// module plus the record's id on an exact number match. Modules are searched
// in parallel; the first match in registry order wins.
func findModuleRecordByNumber(ctx context.Context, pool *pgxpool.Pool, grants ai.Grants, identityID, number string) (globalsearch.AIProvider, string, bool) {
	var candidates []globalsearch.AIProvider
	for _, p := range globalsearch.AIProviders() {
		if scope, ok := grants[p.AI.RecordType]; ok && (scope == ai.ScopeAll || scope == ai.ScopeOwn) {
			candidates = append(candidates, p)
		}
	}
	ids := make([]string, len(candidates))
	var wg sync.WaitGroup
	for i, p := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, _, err := p.Search(ctx, pool, authz.Scope(grants[p.AI.RecordType]), identityID, number, moduleNumberSearchCap)
			if err != nil {
				slog.Warn("ai record lookup: module search failed", "type", p.AI.RecordType, "err", err)
				return
			}
			for _, r := range results {
				if strings.EqualFold(r.Number, number) {
					ids[i] = r.ID
					return
				}
			}
		}()
	}
	wg.Wait()
	for i, id := range ids {
		if id != "" {
			return candidates[i], id, true
		}
	}
	return globalsearch.AIProvider{}, "", false
}

// lookupModuleAnswer answers a record-number question about a module record
// from its AI document, zero LLM calls: the named field when word maps to one,
// the whole document when describe is set, and not-matched otherwise (the
// caller falls through to RAG). found=false means no such record in scope.
func lookupModuleAnswer(ctx context.Context, pool *pgxpool.Pool, grants ai.Grants, identityID, number, word string, describe bool) (ragcore.AskResult, bool) {
	p, id, found := findModuleRecordByNumber(ctx, pool, grants, identityID, number)
	if !found {
		return ragcore.AskResult{}, false
	}
	rec, err := p.AI.Load(ctx, pool, id)
	if err != nil {
		slog.Warn("ai record lookup: module load failed", "type", p.AI.RecordType, "err", err)
		return ragcore.AskResult{}, false
	}
	cite := func(snippet string) []ragcore.Citation {
		return []ragcore.Citation{{SourceType: ai.CorpusRecords, SourceID: id, Snippet: snippet}}
	}
	if key, ok := moduleFieldKeys[word]; ok {
		text, has := lookupValueText(rec.Doc.Core[key])
		if !has {
			return ragcore.AskResult{Answer: fmt.Sprintf("%s doesn't have a %s on file.", number, word), Citations: cite(number)}, true
		}
		return ragcore.AskResult{Answer: fmt.Sprintf("%s's %s is %s.", number, word, text), Citations: cite(fmt.Sprintf("%s: %s", word, text))}, true
	}
	if describe {
		text := describeModuleDoc(rec.Doc)
		return ragcore.AskResult{Answer: fmt.Sprintf("%s: %s", number, text), Citations: cite(number)}, true
	}
	return ragcore.AskResult{}, false
}

// describeModuleDoc renders a module document's populated Core fields, the
// priority ones first, as "Status: Draft; Customer: Acme; ...".
func describeModuleDoc(doc ragcore.RecordDoc) string {
	seen := map[string]bool{}
	var parts []string
	add := func(key string) {
		if seen[key] {
			return
		}
		seen[key] = true
		if text, ok := lookupValueText(doc.Core[key]); ok {
			parts = append(parts, humanFieldName(key)+": "+text)
		}
	}
	for _, k := range doc.Priority {
		if k != "number" {
			add(k)
		}
	}
	rest := make([]string, 0, len(doc.Core))
	for k := range doc.Core {
		rest = append(rest, k)
	}
	sort.Strings(rest)
	for _, k := range rest {
		if k != "number" {
			add(k)
		}
	}
	return strings.Join(parts, "; ")
}

// humanFieldName turns a Core key into a label ("grand_total" -> "Grand total").
func humanFieldName(key string) string {
	s := strings.ReplaceAll(key, "_", " ")
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// unindexedModuleDomains are the globalsearch domains whose modules a user may
// ask about but the assistant cannot yet answer from (no AI hooks). CRM, config
// and users are excluded: CRM has its own path and the rest are not "records".
var unindexedModuleDomains = map[string]bool{"sales": true, "purchases": true, "inventory": true, "finance": true}

// unindexedLex is the noun -> display label map (plus regex) of modules with a
// global-search provider but no AI hooks, so a question about one gets an
// honest "not yet" instead of an answer built from unrelated records.
var unindexedLex = sync.OnceValue(func() *moduleLexicon {
	lex := &moduleLexicon{nounToType: map[string]string{}, labels: map[string]string{}}
	var all []string
	for _, p := range globalsearch.All() {
		if _, indexed := globalsearch.AIByRecordType(p.Key); indexed || !unindexedModuleDomains[p.Domain] {
			continue
		}
		label := strings.ReplaceAll(p.Key, "_", " ")
		lex.labels[p.Key] = label
		for _, form := range []string{label, label + "s"} {
			lex.nounToType[form] = p.Key
			all = append(all, regexp.QuoteMeta(form))
		}
	}
	sort.Slice(all, func(i, j int) bool { return len(all[i]) > len(all[j]) })
	if len(all) > 0 {
		lex.nounRe = regexp.MustCompile(`(?i)\b(?:` + strings.Join(all, "|") + `)\b`)
	}
	return lex
})

// moduleCoverageAnswer replaces a doomed RAG call with a clear statement when a
// non-help question names a module the assistant cannot answer about: one the
// caller has no read grant on, or one that is not indexed yet. A question that
// also names a CRM type ("which customers asked for a quote") is left to RAG.
// found=false means proceed normally.
func moduleCoverageAnswer(question string, grants ai.Grants) (ragcore.AskResult, bool) {
	if intentDataRecordTypeRe.MatchString(question) {
		return ragcore.AskResult{}, false
	}
	empty := func(msg string) (ragcore.AskResult, bool) {
		return ragcore.AskResult{Answer: msg, Citations: []ragcore.Citation{}}, true
	}
	if re := moduleLex().nounRe; re != nil {
		var named, denied []string
		granted := false
		for _, m := range re.FindAllString(question, -1) {
			key, _ := moduleTypeForWord(strings.ReplaceAll(strings.ToLower(m), " ", "_"))
			named = append(named, key)
			if _, ok := grants[key]; ok {
				granted = true
			} else if !containsString(denied, recordTypeLabel(key)) {
				denied = append(denied, recordTypeLabel(key))
			}
		}
		if len(named) > 0 {
			if granted {
				return ragcore.AskResult{}, false
			}
			return empty(noAccessSentence(denied))
		}
	}
	if re := unindexedLex().nounRe; re != nil {
		if m := re.FindString(question); m != "" {
			label := unindexedLex().labels[unindexedLex().nounToType[strings.ToLower(m)]]
			return empty(fmt.Sprintf("I can't answer questions about %s records yet. You can find them in the %s section.", label, label))
		}
	}
	return ragcore.AskResult{}, false
}

// containsString reports whether list holds s.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
