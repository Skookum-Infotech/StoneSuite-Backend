package docextract

import (
	"regexp"
	"strings"
)

var (
	revisionRe  = regexp.MustCompile(`(?i)\b(?:revision\s*[:#]?\s*|rev(?:\.\s*|\s+)[:#]?\s*)([A-Z0-9]{1,3})\b`)
	amendedRe   = regexp.MustCompile(`(?i)\bamended\b`)
	changeOrdRe = regexp.MustCompile(`(?i)\bchange\s+order\s*(?:#|no\.?)?\s*:?\s*([A-Z0-9][A-Z0-9\-]*)`)
	supersedeRe = regexp.MustCompile(`(?i)\b(?:supersedes|replaces)\s+(?:(?:purchase\s+order|sales\s+order|order|po)\s*)?(?:#|no\.?)?\s*:?\s*([A-Z0-9][A-Z0-9\-/]*)`)
	quoteRefRe  = regexp.MustCompile(`(?i)\b(?:your\s+quote|quotation|quote|estimate)\s*(?:#|no\.?|number|ref(?:erence)?)?\s*[:#]?\s*([A-Z0-9][A-Z0-9\-/]*)`)
)

// DetectRevision finds revision / amendment / change-order anchors.
func DetectRevision(pages []PageRows) *Revision {
	var rev *Revision
	for _, p := range pages {
		for _, r := range p.Rows {
			text := r.Text()
			// A change-order number has a digit: "Change Order Buyer Options"
			// in an options table is prose, not a revision.
			if m := changeOrdRe.FindStringSubmatch(text); m != nil && hasDigit(m[1]) {
				rev = &Revision{Label: "Change Order " + strings.ToUpper(m[1])}
			} else if m := revisionRe.FindStringSubmatch(text); m != nil {
				rev = &Revision{Label: "Revision " + strings.ToUpper(m[1])}
			} else if rev == nil && amendedRe.MatchString(text) {
				rev = &Revision{Label: "Amended"}
			}
			if m := supersedeRe.FindStringSubmatch(text); m != nil && hasDigit(m[1]) {
				if rev == nil {
					rev = &Revision{Label: "Amended"}
				}
				rev.ReferencedNumber = m[1]
			}
		}
	}
	return rev
}

// DetectQuoteRef finds a referenced Quote / Estimate number, or "".
func DetectQuoteRef(pages []PageRows) string {
	for _, p := range pages {
		for _, r := range p.Rows {
			for _, m := range quoteRefRe.FindAllStringSubmatch(r.Text(), -1) {
				if hasDigit(m[1]) {
					return m[1]
				}
			}
		}
	}
	return ""
}
