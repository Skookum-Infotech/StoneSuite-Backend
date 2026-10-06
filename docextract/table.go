package docextract

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	addonIndent     = 8.0 // points a description may be indented past the column start
	contGapFactor   = 1.5 // a wrapped row sits within this many pitches of the row above
	alignTol        = 10.0
	minPitch        = 1.0
	snippetEllipsis = "..."
)

var (
	totalsRe  = regexp.MustCompile(`(?i)^(sub\s*-?\s*total|total|grand\s+total|amount\s+due|balance\s+due|order\s+total|invoice\s+total|sales\s+tax|tax|vat)\b`)
	skipRowRe = regexp.MustCompile(`(?i)^(page\s+\d+(\s+of\s+\d+)?|\d+\s+of\s+\d+)$`)
	// notePrefixRe starts a note instead of continuing a description: remarks
	// and contact boilerplate a buyer types into the description column.
	notePrefixRe = regexp.MustCompile(`(?i)^(notes?\b|nb\b|comments?\b|please\b|\*|if\s+you\s+have\b|questions?\b|contact\b)`)
)

// tableResult is the output of parseTable.
type tableResult struct {
	Lines   []Line         // product and add-on lines, in order
	Charges []Line         // freight / discount / deposit rows
	Notes   []Line         // rows with no amount and no qty
	Spans   map[int][2]int // page -> inclusive [first,last] row index of the table region
	Roles   []string       // column role order of the first header
	Found   bool
}

// tableParser carries state across pages.
type tableParser struct {
	lim    Limits
	cols   []column
	active bool
	done   bool
	all    []Line
	count  int // non-note lines seen
	res    tableResult
}

// parseTable finds the line-item table and parses its rows.
func parseTable(pages []PageRows, lim Limits) (tableResult, error) {
	tp := &tableParser{lim: lim.withDefaults(), res: tableResult{Spans: map[int][2]int{}}}
	for _, p := range pages {
		if tp.done {
			break
		}
		if err := tp.page(p); err != nil {
			return tableResult{}, err
		}
	}
	tp.finalize()
	return tp.res, nil
}

// pagePitch is the smallest positive row gap on a page.
func pagePitch(rows []Row) float64 {
	pitch := 0.0
	for i := 1; i < len(rows); i++ {
		g := rows[i-1].Y - rows[i].Y
		if g >= minPitch && (pitch == 0 || g < pitch) {
			pitch = g
		}
	}
	return pitch
}

func (tp *tableParser) page(p PageRows) error {
	// Pitch comes from the table's own rows: letterhead rows a couple of
	// points apart would otherwise make every wrapped description look far away.
	pitch := pagePitch(p.Rows)
	start, last := -1, -1
	if tp.active {
		start = 0
	}
	for i, r := range p.Rows {
		if cols, ok := detectTableHeader(r); ok {
			pitch = pagePitch(p.Rows[i+1:])
			tp.cols = cols
			if !tp.active {
				tp.res.Found = true
				for _, c := range cols {
					tp.res.Roles = append(tp.res.Roles, string(c.role))
				}
			}
			tp.active = true
			start, last = i, i
			continue
		}
		if !tp.active {
			continue
		}
		text := strings.TrimSpace(r.Text())
		if skipRowRe.MatchString(text) {
			last = i
			continue
		}
		cells := assignWords(r, tp.cols)
		if totalsRe.MatchString(text) && !validQty(cells[roleQty]) {
			tp.done = true
			break
		}
		last = i
		var gap float64
		if i > 0 {
			gap = p.Rows[i-1].Y - r.Y
		}
		if err := tp.row(p.Page, i, r, cells, gap, pitch); err != nil {
			return err
		}
	}
	if start >= 0 && last >= start {
		tp.res.Spans[p.Page] = [2]int{start, last}
	}
	return nil
}

// validQty reports whether a qty cell holds a parseable quantity.
func validQty(ws []Word) bool {
	if len(ws) == 0 {
		return false
	}
	_, issue := ParseQty(ws[0].Text)
	return issue == ""
}

var currencySignRe = regexp.MustCompile(`^[-(]?[$€£]$`)

// maxNumericCellWords bounds a numeric cell ("$ 12.00" is two words); a
// longer run in a numeric column is prose that spilled across it.
const maxNumericCellWords = 2

// numericCell reports whether a cell reads as a number: a short run that
// starts with a digit-bearing word, or a currency sign and one. Garbled
// numbers ("1,2O0.00") still count, so they are flagged on their line instead
// of silently becoming a note; prose that spills into a numeric column
// ("form 01-339") does not.
func numericCell(ws []Word) bool {
	if len(ws) == 0 || len(ws) > maxNumericCellWords {
		return false
	}
	first := ws[0].Text
	if currencySignRe.MatchString(first) && len(ws) > 1 {
		first = ws[1].Text
	}
	return hasDigit(first)
}

func (tp *tableParser) row(page, idx int, r Row, cells map[colRole][]Word, gap, pitch float64) error {
	hasNum := numericCell(cells[roleQty]) || numericCell(cells[rolePrice]) || numericCell(cells[roleAmount])
	text := strings.TrimSpace(r.Text())
	if !hasNum {
		tp.noNumbers(page, idx, text, cells, gap, pitch)
		return nil
	}
	ln := buildLine(page, idx, text, cells, tp.cols, tp.lim)
	ln.Kind = tp.kindFor(ln, cells)
	tp.all = append(tp.all, ln)
	tp.count++
	if tp.count > tp.lim.MaxLines {
		return newInputError(FailLineCap, fmt.Sprintf("more than %d lines", tp.lim.MaxLines))
	}
	return nil
}

// noNumbers handles rows without qty, price or amount: wrapped description
// continuations merge into the previous line, anything else is a note.
func (tp *tableParser) noNumbers(page, idx int, text string, cells map[colRole][]Word, gap, pitch float64) {
	if text == "" {
		return
	}
	if n := len(tp.all); n > 0 && tp.all[n-1].Kind != KindNote && !notePrefixRe.MatchString(text) &&
		tp.alignedWithDesc(cells) && (pitch == 0 || gap <= contGapFactor*pitch) {
		prev := &tp.all[n-1]
		merged := strings.TrimSpace(prev.Description.Value + " " + text)
		prev.Description = mergeSnippet(prev.Description, merged)
		return
	}
	note := Line{Kind: KindNote, Description: field(text, text, page, idx, ConfHigh)}
	tp.all = append(tp.all, note)
}

// alignedWithDesc reports whether a row's text starts at the description column.
func (tp *tableParser) alignedWithDesc(cells map[colRole][]Word) bool {
	dc, ok := columnByRole(tp.cols, roleDesc)
	if !ok {
		dc, ok = columnByRole(tp.cols, roleSKU)
	}
	if !ok {
		return true
	}
	first := firstWordX(cells)
	return first >= dc.x0-alignTol && first <= dc.x0+addonIndent+alignTol
}

// firstWordX is the smallest X among all assigned words.
func firstWordX(cells map[colRole][]Word) float64 {
	min := -1.0
	for _, ws := range cells {
		for _, w := range ws {
			if min < 0 || w.X < min {
				min = w.X
			}
		}
	}
	return min
}

// mergeSnippet replaces a field's value, extending its snippet.
func mergeSnippet(f Field, value string) Field {
	f.Value = value
	f.Snippet = clip(value)
	return f
}

// clip truncates text to MaxSnippetLen.
func clip(s string) string {
	if len(s) <= MaxSnippetLen {
		return s
	}
	return s[:MaxSnippetLen-len(snippetEllipsis)] + snippetEllipsis
}

// field builds a document-sourced field.
func field(value, snippet string, page, row int, conf Confidence) Field {
	if value == "" {
		return notFound()
	}
	return Field{Value: value, Source: SourceDocument, Confidence: conf, Snippet: clip(snippet), Page: page, Row: row + 1}
}

// finalize splits raw lines by kind and links add-ons to parents.
func (tp *tableParser) finalize() {
	lastProduct := 0
	for _, ln := range tp.all {
		switch ln.Kind {
		case KindNote:
			tp.res.Notes = append(tp.res.Notes, ln)
		case KindCharge:
			tp.res.Charges = append(tp.res.Charges, ln)
		case KindAddon:
			if lastProduct > 0 {
				ln.ParentLine = lastProduct
			} else {
				ln.Kind = KindProduct
			}
			tp.res.Lines = append(tp.res.Lines, ln)
			if ln.Kind == KindProduct {
				lastProduct = len(tp.res.Lines)
			}
		default:
			tp.res.Lines = append(tp.res.Lines, ln)
			lastProduct = len(tp.res.Lines)
		}
	}
}
