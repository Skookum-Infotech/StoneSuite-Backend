package docextract

import (
	"sort"

	"github.com/ledongthuc/pdf"
)

// The PDF library reads a page's own content stream but ignores the "Do"
// operator, so text drawn inside a form XObject (an imported page, a form
// template's printed labels) is lost. xobjectLayers walks the page content
// just far enough to place each form XObject, and returns the text drawn
// inside each top-level one as a layer, in draw order; the page's own text
// still comes from the library.

const (
	xobjMaxDepth   = 4    // nested form XObjects followed this deep
	xobjMaxInvokes = 2000 // total "Do" calls per page; stops reference loops and bombs
	// One XObject drawn thousands of times would multiply its text; these
	// bound the work per page no matter how the streams are arranged.
	xobjMaxGlyphs    = 200_000
	xobjMaxOps       = 2_000_000
	textSpaceUnits   = 1000 // glyph widths are in thousandths of the font size
	opArgsMatrix     = 6
	opArgsPair       = 2
	opArgsSpacedShow = 3 // aw ac string "
)

type mat [6]float64 // a b c d e f

var identity = mat{1, 0, 0, 1, 0, 0}

// mul returns m × n (apply m, then n).
func (m mat) mul(n mat) mat {
	return mat{
		m[0]*n[0] + m[1]*n[2], m[0]*n[1] + m[1]*n[3],
		m[2]*n[0] + m[3]*n[2], m[2]*n[1] + m[3]*n[3],
		m[4]*n[0] + m[5]*n[2] + n[4], m[4]*n[1] + m[5]*n[3] + n[5],
	}
}

func translate(tx, ty float64) mat { return mat{1, 0, 0, 1, tx, ty} }

type xgstate struct {
	ctm, tm, tlm         mat
	font                 *pdf.Font
	size, tc, tw, th, tl float64
	rise                 float64
}

type xwalker struct {
	layers  [][]pdf.Text
	invokes int
	glyphs  int
	ops     int
}

// spent reports whether the per-page budget is used up; once it is, every
// operator is ignored and no further XObject is entered.
func (w *xwalker) spent() bool { return w.glyphs >= xobjMaxGlyphs || w.ops >= xobjMaxOps }

// xobjectLayers returns the glyphs drawn inside each top-level form XObject
// on the page, one layer per XObject in draw order. A malformed XObject only
// loses its own text, never the page.
func xobjectLayers(page pdf.Page) (out [][]pdf.Text) {
	w := &xwalker{}
	defer func() {
		if recover() != nil {
			out = w.layers
		}
	}()
	// Resources() follows inheritance from the page tree, as the library does.
	w.run(page.V.Key("Contents"), page.Resources(), identity, 0)
	return w.layers
}

// run interprets one content stream; text is kept only below depth 0.
func (w *xwalker) run(strm, res pdf.Value, ctm mat, depth int) {
	g := xgstate{ctm: ctm, tm: identity, tlm: identity, th: 1}
	var stack []xgstate
	fonts := map[string]*pdf.Font{}
	pdf.Interpret(strm, func(stk *pdf.Stack, op string) {
		args := make([]pdf.Value, stk.Len())
		for i := len(args) - 1; i >= 0; i-- {
			args[i] = stk.Pop()
		}
		w.ops++
		if w.spent() {
			return
		}
		switch op {
		case "q":
			stack = append(stack, g)
		case "Q":
			if n := len(stack); n > 0 {
				g, stack = stack[n-1], stack[:n-1]
			}
		case "cm":
			if m, ok := matrixArgs(args); ok {
				g.ctm = m.mul(g.ctm)
			}
		case "Do":
			if len(args) == 1 {
				w.invoke(res.Key("XObject").Key(args[0].Name()), res, g.ctm, depth)
			}
		case "BT":
			g.tm, g.tlm = identity, identity
		case "Tf":
			if len(args) == opArgsPair {
				name := args[0].Name()
				if fonts[name] == nil {
					fonts[name] = &pdf.Font{V: res.Key("Font").Key(name)}
				}
				g.font, g.size = fonts[name], args[1].Float64()
			}
		case "Tc":
			g.tc = argFloat(args)
		case "Tw":
			g.tw = argFloat(args)
		case "Tz":
			g.th = argFloat(args) / 100
		case "TL":
			g.tl = argFloat(args)
		case "Ts":
			g.rise = argFloat(args)
		case "Td", "TD":
			if len(args) == opArgsPair {
				if op == "TD" {
					g.tl = -args[1].Float64()
				}
				g.tlm = translate(args[0].Float64(), args[1].Float64()).mul(g.tlm)
				g.tm = g.tlm
			}
		case "Tm":
			if m, ok := matrixArgs(args); ok {
				g.tm, g.tlm = m, m
			}
		case "T*":
			g.tlm = translate(0, -g.tl).mul(g.tlm)
			g.tm = g.tlm
		case "'", "\"":
			if op == "\"" && len(args) == opArgsSpacedShow {
				g.tw, g.tc = args[0].Float64(), args[1].Float64()
			}
			g.tlm = translate(0, -g.tl).mul(g.tlm)
			g.tm = g.tlm
			if len(args) > 0 {
				w.show(&g, args[len(args)-1].RawString(), depth)
			}
		case "Tj":
			if len(args) == 1 {
				w.show(&g, args[0].RawString(), depth)
			}
		case "TJ":
			if len(args) == 1 {
				arr := args[0]
				for i := 0; i < arr.Len(); i++ {
					if x := arr.Index(i); x.Kind() == pdf.String {
						w.show(&g, x.RawString(), depth)
					} else {
						g.tm = translate(-x.Float64()/textSpaceUnits*g.size*g.th, 0).mul(g.tm)
					}
				}
			}
		}
	})
}

// invoke runs a form XObject in its own coordinate space and resources.
func (w *xwalker) invoke(x, parentRes pdf.Value, ctm mat, depth int) {
	if depth >= xobjMaxDepth || w.invokes >= xobjMaxInvokes || w.spent() || x.Key("Subtype").Name() != "Form" {
		return
	}
	w.invokes++
	if depth == 0 {
		w.layers = append(w.layers, nil)
	}
	m := identity
	if mm := x.Key("Matrix"); mm.Len() == opArgsMatrix {
		for i := range m {
			m[i] = mm.Index(i).Float64()
		}
	}
	res := x.Key("Resources")
	if res.IsNull() {
		res = parentRes
	}
	w.run(x, res, m.mul(ctm), depth+1)
}

// show emits one glyph per decoded rune (only inside an XObject) and advances
// the text matrix, mirroring the library's own page interpreter.
func (w *xwalker) show(g *xgstate, raw string, depth int) {
	if g.font == nil {
		return
	}
	enc := g.font.Encoder()
	n := 0
	for _, ch := range enc.Decode(raw) {
		var advance float64
		if n < len(raw) {
			advance = g.font.Width(int(raw[n]))
		}
		if advance <= 0 {
			// No width table (a standard-14 font): estimate, or every glyph
			// of a phrase lands on the same X.
			advance = estCharWidthFactor * textSpaceUnits
		}
		n++
		trm := mat{g.size * g.th, 0, 0, g.size, 0, g.rise}.mul(g.tm).mul(g.ctm)
		if depth > 0 {
			w.glyphs++
			last := len(w.layers) - 1
			w.layers[last] = append(w.layers[last], pdf.Text{FontSize: trm[0], X: trm[4], Y: trm[5], W: advance / textSpaceUnits * trm[0], S: string(ch)})
		}
		tx := (advance/textSpaceUnits*g.size + g.tc) * g.th
		if ch == ' ' {
			tx += g.tw * g.th
		}
		g.tm = translate(tx, 0).mul(g.tm)
	}
}

func matrixArgs(args []pdf.Value) (mat, bool) {
	if len(args) != opArgsMatrix {
		return mat{}, false
	}
	var m mat
	for i := range m {
		m[i] = args[i].Float64()
	}
	return m, true
}

func argFloat(args []pdf.Value) float64 {
	if len(args) != 1 {
		return 0
	}
	return args[0].Float64()
}

// Layer merge. A form XObject is often a later edit laid over the page's own
// text: it can repeat a label a point or two off (both copies would interleave
// into "SSiinnkk") or white out a word and write a new one on top ("BATH 1"
// shown as "BATH 2").
const (
	layerDupTol     = 3.0 // an identical word this close is the same word
	layerCoverShare = 0.5 // overlapping this share of the narrower word covers it
)

// overlayRows lays one XObject layer's rows over the rows beneath it: an identical
// word nearby is dropped, and a different word it covers is replaced.
func overlayRows(base, over []Row) []Row {
	if len(over) == 0 {
		return base
	}
	for _, or := range over {
		i := nearestRow(base, or.Y)
		if i < 0 {
			base = append(base, or)
			continue
		}
		for _, w := range or.Words {
			base[i].Words = placeOver(base[i].Words, w)
		}
	}
	sort.SliceStable(base, func(i, j int) bool { return base[i].Y > base[j].Y })
	for i := range base {
		ws := base[i].Words
		sort.SliceStable(ws, func(a, b int) bool { return ws[a].X < ws[b].X })
	}
	return base
}

// nearestRow is the row within rowYTolerance of y closest to it; -1 if none.
func nearestRow(rows []Row, y float64) int {
	best, bestD := -1, rowYTolerance
	for i, r := range rows {
		if d := abs(r.Y - y); d <= bestD {
			best, bestD = i, d
		}
	}
	return best
}

// placeOver adds w to a row unless it repeats a word there, dropping any
// different word it covers.
func placeOver(words []Word, w Word) []Word {
	for _, b := range words {
		if b.Text == w.Text && abs(b.X-w.X) <= layerDupTol {
			return words
		}
	}
	out := make([]Word, 0, len(words)+1)
	for _, b := range words {
		if !covers(w, b) {
			out = append(out, b)
		}
	}
	return append(out, w)
}

// covers reports whether a and b overlap horizontally (a heuristic for a
// whited-out word: a stamp laid over printed text would also replace it) by at least
// layerCoverShare of the narrower one.
func covers(a, b Word) bool {
	overlap := min(a.X+a.W, b.X+b.W) - max(a.X, b.X)
	narrow := min(a.W, b.W)
	return narrow > 0 && overlap >= layerCoverShare*narrow
}
