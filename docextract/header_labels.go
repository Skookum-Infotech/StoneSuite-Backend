package docextract

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Label keys.
const (
	keyPO        = "po"
	keyOrderDate = "order_date"
	keyDelivery  = "delivery_date"
	keyBillTo    = "bill_to"
	keyShipTo    = "ship_to"
	keyTerms     = "terms"
	keySubtotal  = "subtotal"
	keyTax       = "tax"
	keyShipping  = "shipping"
	keyDiscount  = "discount"
	keyTotal     = "total"
)

type labelSpec struct {
	key    string
	prio   int
	below  bool // the value may sit in the row beneath the label
	re     *regexp.Regexp
	reject func(rest string) bool
}

var totalRejectRe = regexp.MustCompile(`(?i)^\s*(qty|quantity|units?|items?|lines?|weight|pieces?|pcs)\b`)

// labelSpecs are ordered by claim priority: earlier specs claim their text
// first, so a later, shorter label can never match inside a longer one.
var labelSpecs = []labelSpec{
	{key: keyDelivery, prio: 2, below: true, re: regexp.MustCompile(`(?i)\b(?:delivery\s+date|ship(?:ping)?\s+date|date\s+required|required\s+(?:by|date)|need(?:ed)?\s+by|due\s+date|requested\s+(?:delivery|ship)(?:\s+date)?|delivery\s+by|ship\s+by)\b`)},
	{key: keyOrderDate, prio: 2, below: true, re: regexp.MustCompile(`(?i)\b(?:order\s+date|date\s+ordered|po\s+date|date\s+of\s+order)\b`)},
	{key: keyPO, prio: 2, below: true, re: regexp.MustCompile(`(?i)\b(?:(?:customer|your|buyer'?s?)\s+)?(?:p\.?\s?o\.?|purchase\s+order|order)\s*(?:#|no\b\.?|number)`)},
	{key: keyPO, prio: 1, below: false, re: regexp.MustCompile(`(?i)\b(?:(?:customer|your|buyer'?s?)\s+)?(?:p\.?o\.?|purchase\s+order)\b`)},
	{key: keyShipTo, prio: 2, below: true, re: regexp.MustCompile(`(?i)\b(?:ship(?:ping)?\s+to|deliver(?:y)?\s+to|ship(?:ping)?\s+address|delivery\s+address|job\s*site)\b`)},
	{key: keyBillTo, prio: 3, below: true, re: regexp.MustCompile(`(?i)\b(?:bill(?:ed)?\s+to|sold\s+to|invoice\s+to)\b`)},
	{key: keyBillTo, prio: 1, below: true, re: regexp.MustCompile(`(?i)\b(?:customer(?:\s+name)?|buyer)\b`)},
	{key: keyTerms, prio: 1, below: true, re: regexp.MustCompile(`(?i)\b(?:payment\s+terms|terms(?:\s+of\s+payment)?|terms)\b`)},
	{key: keySubtotal, prio: 1, below: true, re: regexp.MustCompile(`(?i)\bsub\s*-?\s*total\b`)},
	{key: keyTotal, prio: 2, below: true, re: regexp.MustCompile(`(?i)\b(?:grand\s+total|order\s+total|invoice\s+total|total\s+due|amount\s+due|balance\s+due)\b`)},
	{key: keyTotal, prio: 1, below: true, re: regexp.MustCompile(`(?i)\btotal(?:\s+amount)?\b`), reject: func(rest string) bool { return totalRejectRe.MatchString(rest) }},
	{key: keyTax, prio: 1, below: true, re: regexp.MustCompile(`(?i)\b(?:sales\s+tax|taxes|tax|vat|gst)\b`)},
	{key: keyShipping, prio: 1, below: true, re: regexp.MustCompile(`(?i)\b(?:shipping(?:\s*(?:&|and)\s*handling)?|freight|handling|delivery\s+(?:fee|charge))\b`)},
	{key: keyDiscount, prio: 1, below: true, re: regexp.MustCompile(`(?i)\bdiscount\b`)},
	{key: keyOrderDate, prio: 1, below: true, re: regexp.MustCompile(`(?i)\bdate\b`)},
}

// hrow is one header-region row with word offsets into its text.
type hrow struct {
	page, idx int
	row       Row
	text      string
	offs      []int
}

func newHRow(page, idx int, r Row) hrow {
	h := hrow{page: page, idx: idx, row: r}
	var b strings.Builder
	for i, w := range r.Words {
		if i > 0 {
			b.WriteByte(' ')
		}
		h.offs = append(h.offs, b.Len())
		b.WriteString(w.Text)
	}
	h.text = b.String()
	return h
}

// wordAt returns the index of the word containing text offset off.
func (h hrow) wordAt(off int) int {
	i := sort.Search(len(h.offs), func(i int) bool { return h.offs[i] > off }) - 1
	if i < 0 {
		return 0
	}
	return i
}

type labelHit struct {
	spec       *labelSpec
	ri         int // index into the flattened header rows
	start, end int
	nextStart  int
	x, nextX   float64
}

// findHits locates every label in the flattened rows.
func findHits(rows []hrow) []labelHit {
	var all []labelHit
	for ri, h := range rows {
		var claimed [][2]int
		var hits []labelHit
		for si := range labelSpecs {
			sp := &labelSpecs[si]
			for _, m := range sp.re.FindAllStringIndex(h.text, -1) {
				if overlaps(claimed, m[0], m[1]) {
					continue
				}
				if sp.reject != nil && sp.reject(h.text[m[1]:]) {
					continue
				}
				claimed = append(claimed, [2]int{m[0], m[1]})
				hits = append(hits, labelHit{spec: sp, ri: ri, start: m[0], end: m[1], x: h.row.Words[h.wordAt(m[0])].X})
			}
		}
		sort.SliceStable(hits, func(a, b int) bool { return hits[a].start < hits[b].start })
		hits = dropEchoes(hits)
		for i := range hits {
			hits[i].nextStart, hits[i].nextX = len(h.text), math.Inf(1)
			if i+1 < len(hits) {
				hits[i].nextStart = hits[i+1].start
				hits[i].nextX = hits[i+1].x
			}
		}
		all = append(all, hits...)
	}
	return all
}

func overlaps(claimed [][2]int, s, e int) bool {
	for _, c := range claimed {
		if s < c[1] && e > c[0] {
			return true
		}
	}
	return false
}

const segTrim = " :#-\t"

// inlineSeg is the text after a label up to the next label on the row.
func inlineSeg(rows []hrow, h labelHit) string {
	return strings.Trim(rows[h.ri].text[h.end:h.nextStart], segTrim)
}

// belowSeg is the text in the next adjacent row within the label's x-range.
func belowSeg(rows []hrow, h labelHit) string {
	if h.ri+1 >= len(rows) || !adjacent(rows[h.ri], rows[h.ri+1]) {
		return ""
	}
	var parts []string
	for _, w := range rows[h.ri+1].row.Words {
		if w.X >= h.x-alignTol && w.X < h.nextX {
			parts = append(parts, w.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func adjacent(a, b hrow) bool { return a.page == b.page && b.idx == a.idx+1 }

// hitsFor returns hits for a key ordered best first: priority, then document order
// (or reverse document order for totals, where the last figure wins).
func hitsFor(hits []labelHit, key string, last bool) []labelHit {
	var out []labelHit
	for _, h := range hits {
		if h.spec.key == key {
			out = append(out, h)
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].spec.prio != out[b].spec.prio {
			return out[a].spec.prio > out[b].spec.prio
		}
		if last {
			return out[a].ri > out[b].ri
		}
		return out[a].ri < out[b].ri
	})
	return out
}

// maxEchoGap is the largest character gap at which a repeated label of the
// same key is treated as part of the previous label's value ("PO No. PO-7782").
const maxEchoGap = 2

// dropEchoes removes a hit that directly follows a hit of the same key.
func dropEchoes(hits []labelHit) []labelHit {
	var out []labelHit
	for _, h := range hits {
		if n := len(out); n > 0 && out[n-1].spec.key == h.spec.key && h.start-out[n-1].end <= maxEchoGap {
			continue
		}
		out = append(out, h)
	}
	return out
}
