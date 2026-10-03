package docextractjob

import (
	"regexp"
	"sort"
	"strings"
)

// nameStopwords are legal-form and filler words that carry no identity.
var nameStopwords = map[string]struct{}{
	"the": {}, "and": {}, "of": {}, "inc": {}, "llc": {}, "ltd": {}, "co": {}, "corp": {},
	"corporation": {}, "company": {}, "incorporated": {}, "limited": {}, "lp": {}, "llp": {},
}

// minPrefilterTokenLen is the shortest token used for the candidate prefilter.
const minPrefilterTokenLen = 3

var nameTokenSepRe = regexp.MustCompile(`[^a-z0-9]+`)

// SignificantTokens lower-cases a name, splits it on non-alphanumerics and
// drops legal-form stopwords ("Inc", "LLC", "The"), keeping first-seen order.
func SignificantTokens(name string) []string {
	var out []string
	for _, t := range nameTokenSepRe.Split(strings.ToLower(name), -1) {
		if t == "" {
			continue
		}
		if _, stop := nameStopwords[t]; stop {
			continue
		}
		out = append(out, t)
	}
	return out
}

// TokenDice is the Sorensen-Dice similarity of the two names' significant
// token sets, in [0,1]. Two names with no significant tokens score 0.
func TokenDice(a, b string) float64 {
	ta, tb := tokenSet(SignificantTokens(a)), tokenSet(SignificantTokens(b))
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	shared := 0
	for t := range ta {
		if _, ok := tb[t]; ok {
			shared++
		}
	}
	return 2 * float64(shared) / float64(len(ta)+len(tb))
}

// tokenSet turns a token slice into a set.
func tokenSet(tokens []string) map[string]struct{} {
	m := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		m[t] = struct{}{}
	}
	return m
}

// prefilterToken is the first significant token long enough to prefilter on, or "".
func prefilterToken(name string) string {
	for _, t := range SignificantTokens(name) {
		if len(t) >= minPrefilterTokenLen {
			return t
		}
	}
	return ""
}

// ScoredName is one candidate name with its similarity score.
type ScoredName struct {
	Index int // index into the caller's candidate slice
	Score float64
}

// RankByDice scores every candidate name against target and returns the top
// n by descending score (ties keep input order); zero scores are dropped.
func RankByDice(target string, candidates []string, n int) []ScoredName {
	scored := make([]ScoredName, 0, len(candidates))
	for i, c := range candidates {
		if s := TokenDice(target, c); s > 0 {
			scored = append(scored, ScoredName{Index: i, Score: s})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if len(scored) > n {
		scored = scored[:n]
	}
	return scored
}
