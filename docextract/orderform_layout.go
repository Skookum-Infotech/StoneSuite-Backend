package docextract

import (
	"sort"
	"strings"
	"unicode"
)

// Layout helpers for the order-form profile: label and heading matching, and
// cutting rows into the form's area columns.

// faucetPositions names the printed positions the ticks sit on ("4\" C 4\"").
func faucetPositions(ticks, marks []Word) string {
	var out []string
	for _, t := range ticks {
		if t.Text != CheckedMark {
			continue
		}
		best, bestD := "", float64(ofMarkMaxDistance)
		for _, m := range marks {
			if d := abs((m.X + m.W/2) - (t.X + t.W/2)); d < bestD {
				best, bestD = m.Text, d
			}
		}
		if best != "" {
			out = append(out, best)
		}
	}
	return strings.Join(out, " ")
}

// matchLabel matches a label phrase at the start of run, comparing letters
// only so spacing ("Si nk Model") and run-in hint text ("Instructio*n*sF")
// don't matter. It returns the label key and how many words it used.
func matchLabel(run []Word) (string, int) {
	var b strings.Builder
	for n := 1; n <= ofMaxLabelWords && n <= len(run); n++ {
		b.WriteString(letters(run[n-1].Text))
		got := b.String()
		for _, key := range ofLabels {
			if got == key || (len(key) >= ofMinPrefixLabel && strings.HasPrefix(got, key)) {
				return key, n
			}
		}
	}
	return "", 0
}

// headingLen is how many leading words form an area heading: an upper-case
// area word, then upper-case words, numbers, "/" or "AREA:".
func headingLen(run []Word) int {
	if len(run) == 0 || !isUpperWord(run[0].Text) || !ofAreaWords[letters(run[0].Text)] {
		return 0
	}
	n := 1
	for n < len(run) && (isUpperWord(run[n].Text) || run[n].Text == "/" || isDigits(run[n].Text)) &&
		run[n].X-(run[n-1].X+run[n-1].W) <= ofHeadingGap {
		n++
	}
	return n
}

func rowHasHeading(r Row) bool {
	for i, w := range r.Words {
		if !w.Form && headingLen(r.Words[i:]) > 0 {
			return true
		}
	}
	return false
}

// columnStarts are the left edges of the area columns on a page, from the
// headings' X positions.
func columnStarts(rows []Row) []float64 {
	var xs []float64
	for _, r := range rows {
		for i := 0; i < len(r.Words); i++ {
			if n := headingLen(r.Words[i:]); !r.Words[i].Form && n > 0 {
				xs = append(xs, r.Words[i].X)
				i += n - 1
			}
		}
	}
	sort.Float64s(xs)
	var starts []float64
	for _, x := range xs {
		if len(starts) == 0 || x-starts[len(starts)-1] > ofColumnMerge {
			starts = append(starts, x)
		}
	}
	return starts
}

// splitColumns cuts a row's words at the column starts; index 0 holds any
// words left of the first column.
func splitColumns(words []Word, starts []float64) [][]Word {
	out := make([][]Word, len(starts)+1)
	for _, w := range words {
		ci := 0
		for i, s := range starts {
			if w.X+ofColumnSlack >= s {
				ci = i + 1
			}
		}
		out[ci] = append(out[ci], w)
	}
	return out
}

func letters(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isUpperWord(s string) bool {
	hasLetter := false
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
		hasLetter = hasLetter || unicode.IsLetter(r)
	}
	return hasLetter
}

func isDigits(s string) bool {
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return s != ""
}
