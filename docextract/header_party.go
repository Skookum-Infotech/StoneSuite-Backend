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
	// Line spacing is learned between value lines: the gap from a label row
	// down to its first value line is often wider and says nothing about
	// where the block ends.
	prevIsValue := len(lines) > 0
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
		if prevIsValue {
			if firstGap == 0 {
				firstGap = gap
			} else if math.Abs(gap) > gapStopFactor*math.Abs(firstGap) {
				break
			}
		}
		lines = append(lines, strings.Join(parts, " "))
		prev, prevIsValue = cur, true
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

const (
	letterheadWordGap = 24.0 // points between words of one letterhead name
	letterheadMaxDY   = 16.0 // points between letterhead block lines
)

// extractLetterhead reads the issuer block at the top-left of the first page:
// the company name and the address lines under it. On a customer's PO with no
// Bill To / Customer label the issuer is the customer, so this is the fallback
// for the customer and bill-to fields, at check confidence because it is
// inferred. A title ("Purchase Order"), a labelled row or our own company is
// never taken, and the block must look like an issuer: a name with at least
// one address line holding a number (street or ZIP) under it.
func extractLetterhead(rows []hrow, hits []labelHit, own []string) (name, block Field) {
	name, block = notFound(), notFound()
	if len(rows) == 0 {
		return name, block
	}
	top := rows[0]
	words := top.row.Words
	if len(words) == 0 {
		return name, block
	}
	end := 1
	for end < len(words) && words[end].X-(words[end-1].X+words[end-1].W) <= letterheadWordGap {
		end++
	}
	lo, hi := words[0].X-alignTol, math.Inf(1)
	if end < len(words) {
		hi = words[end].X
	}
	first := cleanName(joinWords(words[:end]))
	if !letterheadName(first, own) || labelInRange(hits, 0, lo, hi) {
		return name, block
	}
	lines := []string{first}
	lastY := top.row.Y
	prev := top
	for ri := 1; ri < len(rows) && len(lines) < maxPartyLines; ri++ {
		cur := rows[ri]
		if !adjacent(prev, cur) || lastY-cur.row.Y > letterheadMaxDY {
			break
		}
		prev = cur
		var parts []string
		for _, w := range cur.row.Words {
			if w.X >= lo && w.X < hi {
				parts = append(parts, w.Text)
			}
		}
		if len(parts) == 0 {
			continue // e.g. a boxed PO number to the right, between address lines
		}
		if labelInRange(hits, ri, lo, hi) {
			break
		}
		lines = append(lines, strings.Join(parts, " "))
		lastY = cur.row.Y
	}
	if !hasAddressLine(lines[1:]) {
		return name, block
	}
	name = field(first, top.text, top.page, top.idx, ConfCheck)
	block = field(strings.Join(lines, "\n"), top.text, top.page, top.idx, ConfCheck)
	return name, block
}

// hasAddressLine reports whether any line carries a number, as a street
// address or ZIP code does.
func hasAddressLine(lines []string) bool {
	for _, l := range lines {
		if hasDigit(l) {
			return true
		}
	}
	return false
}

// letterheadName reports whether text can be an issuer's company name: it has
// letters, is not a document title and is not our own company.
func letterheadName(s string, own []string) bool {
	if !strings.ContainsFunc(s, unicode.IsLetter) || isOwnCompany(s, own) {
		return false
	}
	low := strings.ToLower(s)
	for _, k := range classKeywords {
		if strings.HasPrefix(low, k.phrase) {
			return false
		}
	}
	return true
}

// joinWords joins word texts with single spaces.
func joinWords(ws []Word) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = w.Text
	}
	return strings.Join(parts, " ")
}
