package docextract

import (
	"regexp"
	"strings"
	"unicode"
)

const (
	minDigitWords = 6    // a page needs this many digit-bearing words to be kept on density
	minDigitRatio = 0.10 // ...and at least this share of all words
)

var moneyTokenRe = regexp.MustCompile(`[$€£]\s?\d|\d[\d,]*\.\d{2}\b`)

// PrunePages drops pages that carry no digit, currency or table density (for
// example terms-and-conditions pages) so they never reach the parser or the
// LLM. Original page numbers are kept; the first page is always kept.
func PrunePages(pages []PageRows) []PageRows {
	var kept []PageRows
	for i, p := range pages {
		if i == 0 || pageHasDensity(p) || pageHasFormValues(p) {
			kept = append(kept, p)
		}
	}
	return kept
}

// pageHasDensity reports whether a page looks like part of an order document.
func pageHasDensity(p PageRows) bool {
	var words, digitWords int
	for _, r := range p.Rows {
		if _, ok := detectTableHeader(r); ok {
			return true
		}
		for _, w := range r.Words {
			words++
			if hasDigit(w.Text) {
				digitWords++
			}
		}
		if moneyTokenRe.MatchString(r.Text()) {
			digitWords++
		}
	}
	if words == 0 {
		return false
	}
	return digitWords >= minDigitWords && float64(digitWords)/float64(words) >= minDigitRatio
}

// hasDigit reports whether s contains an ASCII digit.
func hasDigit(s string) bool {
	return strings.IndexFunc(s, unicode.IsDigit) >= 0
}

// pageHasFormValues reports whether something was typed into a fillable field
// on the page; a filled form page is always part of the order.
func pageHasFormValues(p PageRows) bool {
	for _, r := range p.Rows {
		for _, w := range r.Words {
			if w.Form {
				return true
			}
		}
	}
	return false
}
