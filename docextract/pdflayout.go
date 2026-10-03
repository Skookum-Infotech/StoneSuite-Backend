package docextract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

const (
	rowYTolerance = 2.5  // points; glyphs within this Y belong to one row
	wordGapFactor = 0.25 // gap > factor*fontSize starts a new word
	// estCharWidthFactor is the assumed average advance (in font-size units) of
	// a glyph whose font has no width table — about Helvetica's average.
	estCharWidthFactor = 0.5
	mojibakeThreshold  = 0.30 // share of junk runes that makes a text layer unreadable
	minRunesForRatio   = 20   // below this many runes the ratio is not meaningful
	encryptMarker      = "/Encrypt"
	sigByteRange       = "/ByteRange"
	sigMarker          = "/Sig"
	unsupportedMarker  = "unsupported PDF"
)

// PDFResult is the layout of a parsed PDF.
type PDFResult struct {
	Pages      []PageRows
	Signed     bool
	Restricted bool
	Warnings   []string
}

// ParsePDF extracts positioned rows from every page. The page cap is enforced
// before any page is read and ctx is checked between pages. Library panics are
// recovered into a corrupt InputError.
func ParsePDF(ctx context.Context, b []byte, lim Limits) (res PDFResult, err error) {
	lim = lim.withDefaults()
	defer func() {
		if x := recover(); x != nil {
			res = PDFResult{}
			err = newInputError(FailCorrupt, fmt.Sprintf("pdf parser panic: %v", x))
		}
	}()
	res.Signed = bytes.Contains(b, []byte(sigByteRange)) && bytes.Contains(b, []byte(sigMarker))
	r, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return PDFResult{}, classifyPDFOpenError(err)
	}
	res.Restricted = bytes.Contains(b, []byte(encryptMarker))
	n := r.NumPage()
	if n <= 0 {
		return PDFResult{}, newInputError(FailCorrupt, "pdf has no pages")
	}
	if n > lim.MaxPages {
		return PDFResult{}, newInputError(FailPageCap, fmt.Sprintf("%d pages exceeds the cap of %d", n, lim.MaxPages))
	}
	var runes, junk int
	for i := 1; i <= n; i++ {
		if cerr := ctx.Err(); cerr != nil {
			return PDFResult{}, fmt.Errorf("pdf parse cancelled: %w", cerr)
		}
		rows, perr := readPage(r, i)
		if perr != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("page %d couldn't be read", i))
			continue
		}
		for _, row := range rows {
			for _, w := range row.Words {
				for _, c := range w.Text {
					runes++
					if isJunkRune(c) {
						junk++
					}
				}
			}
		}
		res.Pages = append(res.Pages, PageRows{Page: i, Rows: rows})
	}
	if runes == 0 {
		return PDFResult{}, newInputError(FailScanned, "no text layer found")
	}
	if runes >= minRunesForRatio && float64(junk)/float64(runes) > mojibakeThreshold {
		return PDFResult{}, newInputError(FailUnreadableText, "text layer is unreadable")
	}
	return res, nil
}

// classifyPDFOpenError maps a library open error to an InputError.
func classifyPDFOpenError(err error) *InputError {
	switch {
	case errors.Is(err, pdf.ErrInvalidPassword):
		return newInputError(FailPasswordProtected, "a password is required to open this PDF")
	case strings.Contains(err.Error(), unsupportedMarker):
		return newInputError(FailUnsupportedEncryption, err.Error())
	default:
		return newInputError(FailCorrupt, err.Error())
	}
}

// isJunkRune is true for replacement, private-use and control characters.
func isJunkRune(c rune) bool {
	if c == unicode.ReplacementChar {
		return true
	}
	if c >= 0xE000 && c <= 0xF8FF {
		return true
	}
	return unicode.IsControl(c) && c != '\t' && c != '\n' && c != '\r'
}

// readPage reads one page, converting library panics into an error.
func readPage(r *pdf.Reader, num int) (rows []Row, err error) {
	defer func() {
		if x := recover(); x != nil {
			rows = nil
			err = fmt.Errorf("page %d: %v", num, x)
		}
	}()
	p := r.Page(num)
	if p.V.IsNull() {
		return nil, fmt.Errorf("page %d: not found", num)
	}
	return buildRows(p.Content().Text), nil
}

// buildRows groups per-glyph text into Y-ordered rows of X-ordered words.
func buildRows(glyphs []pdf.Text) []Row {
	cleaned := make([]pdf.Text, 0, len(glyphs))
	for _, g := range glyphs {
		if g.S == "\n" || g.S == "" {
			continue
		}
		cleaned = append(cleaned, g)
	}
	sort.SliceStable(cleaned, func(i, j int) bool { return cleaned[i].Y > cleaned[j].Y })
	var rows [][]pdf.Text
	var ys []float64
	for _, g := range cleaned {
		if n := len(rows); n > 0 && ys[n-1]-g.Y <= rowYTolerance {
			rows[n-1] = append(rows[n-1], g)
			continue
		}
		rows = append(rows, []pdf.Text{g})
		ys = append(ys, g.Y)
	}
	out := make([]Row, 0, len(rows))
	for i, rg := range rows {
		sort.SliceStable(rg, func(a, b int) bool { return rg[a].X < rg[b].X })
		if words := buildWords(rg); len(words) > 0 {
			out = append(out, Row{Y: ys[i], Words: words})
		}
	}
	return out
}

// buildWords splits X-ordered glyphs into words at spaces and wide gaps.
func buildWords(gs []pdf.Text) []Word {
	var words []Word
	var cur strings.Builder
	var x0, end float64
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, Word{X: x0, W: end - x0, Text: cur.String()})
			cur.Reset()
		}
	}
	for _, g := range gs {
		if strings.TrimSpace(g.S) == "" {
			flush()
			continue
		}
		if cur.Len() > 0 && g.X-end > wordGapFactor*g.FontSize {
			flush()
		}
		if cur.Len() == 0 {
			x0 = g.X
		}
		cur.WriteString(g.S)
		end = g.X + glyphWidth(g)
	}
	flush()
	return words
}

// glyphWidth is the run's advance width. Fonts without width tables report 0,
// which would measure the next gap from the run's start and split kerned
// words ("T imesheet"); estimate an average advance instead.
func glyphWidth(g pdf.Text) float64 {
	if g.W > 0 {
		return g.W
	}
	return float64(utf8.RuneCountInString(g.S)) * estCharWidthFactor * g.FontSize
}
