package docextract

import (
	"math"
	"regexp"
	"strings"
	"unicode"
)

const (
	maxPartyLines = 5
	gapStopFactor = 1.8
)

var nonAlnumRe = regexp.MustCompile(`[^a-z0-9]+`)

// normName lower-cases and strips punctuation for name comparison.
func normName(s string) string {
	return strings.Trim(nonAlnumRe.ReplaceAllString(strings.ToLower(s), " "), " ")
}

// isOwnCompany reports whether a name refers to one of our own company names.
func isOwnCompany(name string, own []string) bool {
	n := normName(name)
	if n == "" {
		return false
	}
	for _, o := range own {
		on := normName(o)
		if on != "" && (strings.Contains(n, on) || strings.Contains(on, n)) {
			return true
		}
	}
	return false
}

// extractParty reads the multi-line block under a Bill To / Ship To style label.
// When wantName is set the first line is returned as the party name.
func extractParty(rows []hrow, hits []labelHit, key string, own []string, wantName bool) (name, block Field) {
	name, block = notFound(), notFound()
	for _, h := range hitsFor(hits, key, false) {
		lines := partyLines(rows, hits, h)
		if len(lines) == 0 || isOwnCompany(lines[0], own) {
			continue
		}
		r := rows[h.ri]
		block = field(strings.Join(lines, "\n"), r.text, r.page, r.idx, ConfHigh)
		if wantName {
			name = field(cleanName(lines[0]), r.text, r.page, r.idx, ConfHigh)
		}
		return name, block
	}
	return name, block
}

// cleanName trims trailing punctuation from a party name line.
func cleanName(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == ',' || r == ';' || r == ':' })
}

// partyLines collects the block lines for one label hit.
func partyLines(rows []hrow, hits []labelHit, h labelHit) []string {
	var lines []string
	if seg := inlineSeg(rows, h); seg != "" {
		lines = append(lines, seg)
	}
	lo, hi := h.x-alignTol, h.nextX
	prev := rows[h.ri]
	firstGap := 0.0
	for ri := h.ri + 1; ri < len(rows) && len(lines) < maxPartyLines; ri++ {
		cur := rows[ri]
		if !adjacent(prev, cur) {
			break
		}
		var parts []string
		for _, w := range cur.row.Words {
			if w.X >= lo && w.X < hi {
				parts = append(parts, w.Text)
			}
		}
		if len(parts) == 0 || labelInRange(hits, ri, lo, hi) {
			break
		}
		gap := prev.row.Y - cur.row.Y
		if firstGap == 0 {
			firstGap = gap
		} else if math.Abs(gap) > gapStopFactor*math.Abs(firstGap) {
			break
		}
		lines = append(lines, strings.Join(parts, " "))
		prev = cur
	}
	return lines
}

// labelInRange reports whether any label on row ri starts inside [lo,hi).
func labelInRange(hits []labelHit, ri int, lo, hi float64) bool {
	for _, h := range hits {
		if h.ri == ri && h.x >= lo && h.x < hi {
			return true
		}
	}
	return false
}
