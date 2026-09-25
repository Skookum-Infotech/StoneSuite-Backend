package pgvector

import (
	"strings"
	"unicode"
)

// stopwords are dropped from a question before it becomes a full-text query.
// websearch_to_tsquery ANDs every term and the 'simple' config has no
// stopword list of its own, so without this "what is the status of Acme"
// requires a row to contain "what", "is", "the" and "of" — natural-language
// questions then match nothing and the arm contributes only for bare
// keyword queries. Question words and conversational filler are included for
// the same reason. "or" is here too: websearch_to_tsquery treats it as an
// operator.
var stopwords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be been but by can could did do does doing for from
		had has have having how i if in into is it its me my of on or our ours please should so some
		than that the their them then there these they this those to us was we were what when where
		which who whom whose why will with would you your yours about any all also am just show tell
		give find list get many much know let lets need want see look looking information info details`) {
		stopwords[w] = true
	}
}

// LexicalQuery reduces a natural-language question to the content words a
// full-text search should require: surrounding punctuation stripped (inner
// hyphens and dots kept, so INC-2023-Q4-011 or acme.com survive), stopwords
// dropped, and websearch operators (leading "-", quotes) neutralised. Returns
// "" when nothing meaningful is left.
func LexicalQuery(question string) string {
	var terms []string
	for _, raw := range strings.Fields(question) {
		term := strings.TrimFunc(raw, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if term == "" || stopwords[strings.ToLower(term)] {
			continue
		}
		terms = append(terms, term)
	}
	return strings.Join(terms, " ")
}
