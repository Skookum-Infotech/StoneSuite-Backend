package docextract

import (
	"sort"
	"strings"

	"github.com/ledongthuc/pdf"
)

// A fillable (AcroForm) PDF keeps what was typed into it in widget
// annotations, not in the page content stream, so a text read of the page sees
// only the printed labels. These helpers lay each filled value back onto the
// page as positioned words marked Form, so every later stage reads a filled
// form the way a person sees it.

// CheckedMark is the word a ticked checkbox or radio button becomes.
const CheckedMark = "[X]"

const (
	formMaxParentDepth = 8   // /Parent chains are short; this also stops cycles
	formHiddenFlag     = 2   // annotation /F bit 2: hidden
	formMaxFontSize    = 10. // auto-sized fields render at about this size
	formFontFactor     = 0.7 // font size as a share of the widget height
	formBaselineFactor = 0.3 // baseline sits this far up the widget
	formRowSlack       = 2.0 // a row this close outside the widget still owns it
	formOffState       = "Off"
	formTypeButton     = "Btn"
	formTypeText       = "Tx"
	formTypeChoice     = "Ch"
	formRectLen        = 4
	optPairLen         = 2 // an /Opt entry [export display]
	// Caps on what a hostile file can make us walk: real forms have a few
	// hundred widgets per page and a few dozen options per list.
	formMaxWidgets = 2000
	formMaxOptions = 500
	formMaxChosen  = 50
)

// formValue is one filled widget: its box on the page and its shown text.
type formValue struct {
	x0, y0, x1, y1 float64
	text           string
}

// pageFormValues returns the filled, visible widgets on one page.
func pageFormValues(page pdf.Value) []formValue {
	annots := page.Key("Annots")
	var out []formValue
	for i := 0; i < min(annots.Len(), formMaxWidgets); i++ {
		a := annots.Index(i)
		if a.Key("Subtype").Name() != "Widget" || a.Key("F").Int64()&formHiddenFlag != 0 {
			continue
		}
		text := widgetText(a)
		r := a.Key("Rect")
		if text == "" || r.Len() != formRectLen {
			continue
		}
		x0, y0, x1, y1 := r.Index(0).Float64(), r.Index(1).Float64(), r.Index(2).Float64(), r.Index(3).Float64()
		out = append(out, formValue{x0: min(x0, x1), y0: min(y0, y1), x1: max(x0, x1), y1: max(y0, y1), text: text})
	}
	return out
}

// inherited looks a field key up on the widget, then up its /Parent chain
// (a radio group or a renamed field keeps /FT and /V on the parent).
func inherited(a pdf.Value, key string) pdf.Value {
	for d := 0; d < formMaxParentDepth && !a.IsNull(); d++ {
		if v := a.Key(key); !v.IsNull() {
			return v
		}
		a = a.Key("Parent")
	}
	return pdf.Value{}
}

// widgetText is what the widget shows: a ticked box is CheckedMark, a text or
// choice field its value; anything else (unticked, blank, push button) is "".
func widgetText(a pdf.Value) string {
	switch inherited(a, "FT").Name() {
	case formTypeButton:
		// A widget's own appearance state says whether *this* box is on; a
		// radio group's shared /V cannot.
		state := a.Key("AS").Name()
		if a.Key("AS").IsNull() {
			state = inherited(a, "V").Name()
		}
		if state != "" && state != formOffState {
			return CheckedMark
		}
		return ""
	case formTypeText:
		return valueText(inherited(a, "V"))
	case formTypeChoice:
		return choiceText(inherited(a, "V"), inherited(a, "Opt"), inherited(a, "I"))
	default:
		return ""
	}
}

// choiceText is what a list or combo box shows. Options may be
// [export, display] pairs and /V holds the export value, which can be blank
// for a real choice (an option exported as "" that shows 4"). The selected
// indexes /I settle that, as a viewer does; without them a blank is blank.
func choiceText(v, opts, idx pdf.Value) string {
	if idx.Kind() == pdf.Array && idx.Len() > 0 && opts.Len() > 0 {
		parts := make([]string, 0, idx.Len())
		for i := 0; i < min(idx.Len(), formMaxChosen); i++ {
			if k := int(idx.Index(i).Int64()); k >= 0 && k < min(opts.Len(), formMaxOptions) {
				if t := optionDisplay(opts.Index(k)); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, ", ")
	}
	if v.Kind() == pdf.Array {
		parts := make([]string, 0, v.Len())
		for i := 0; i < min(v.Len(), formMaxChosen); i++ {
			if t := choiceText(v.Index(i), opts, pdf.Value{}); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, ", ")
	}
	exported := valueText(v)
	if exported == "" {
		return ""
	}
	for i := 0; i < min(opts.Len(), formMaxOptions); i++ {
		if o := opts.Index(i); o.Kind() == pdf.Array && o.Len() == optPairLen && valueText(o.Index(0)) == exported {
			return valueText(o.Index(1))
		}
	}
	return exported
}

// optionDisplay is an /Opt entry's shown text: the string itself, or the
// display half of an [export display] pair.
func optionDisplay(o pdf.Value) string {
	if o.Kind() == pdf.Array && o.Len() == optPairLen {
		return valueText(o.Index(1))
	}
	return valueText(o)
}

// valueText flattens a field value (string, name, or a multi-select array) to
// single-spaced text.
func valueText(v pdf.Value) string {
	var s string
	switch v.Kind() {
	case pdf.String:
		s = v.Text()
	case pdf.Name:
		s = v.Name()
	case pdf.Array:
		parts := make([]string, 0, v.Len())
		for i := 0; i < min(v.Len(), formMaxChosen); i++ {
			if t := valueText(v.Index(i)); t != "" {
				parts = append(parts, t)
			}
		}
		s = strings.Join(parts, ", ")
	}
	return strings.Join(strings.Fields(s), " ")
}

// mergeFormWords places each filled value on the row that runs through its
// widget (the label's row), or on a new row of its own, keeping rows Y-ordered
// and words X-ordered.
func mergeFormWords(rows []Row, values []formValue) []Row {
	if len(values) == 0 {
		return rows
	}
	for _, fv := range values {
		words := formWords(fv)
		if i := rowForWidget(rows, fv); i >= 0 {
			rows[i].Words = append(rows[i].Words, words...)
			continue
		}
		rows = append(rows, Row{Y: fv.y0 + (fv.y1-fv.y0)*formBaselineFactor, Words: words})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Y > rows[j].Y })
	for i := range rows {
		ws := rows[i].Words
		sort.SliceStable(ws, func(a, b int) bool { return ws[a].X < ws[b].X })
	}
	return rows
}

// rowForWidget is the row whose baseline lies inside the widget's height and
// is closest to where the value's own baseline would be; -1 when none.
func rowForWidget(rows []Row, fv formValue) int {
	base := fv.y0 + (fv.y1-fv.y0)*formBaselineFactor
	best, bestD := -1, 0.0
	for i, r := range rows {
		if r.Y < fv.y0-formRowSlack || r.Y > fv.y1+formRowSlack {
			continue
		}
		if d := abs(r.Y - base); best < 0 || d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

// formWords splits a value into words laid left to right from the widget's
// left edge, squeezed to stay inside the widget so a long value never spills
// into the next column.
func formWords(fv formValue) []Word {
	size := min(formMaxFontSize, (fv.y1-fv.y0)*formFontFactor)
	if size <= 0 {
		size = formMaxFontSize
	}
	parts := strings.Fields(fv.text)
	widths := make([]float64, len(parts))
	space := estCharWidthFactor * size
	total := 0.0
	for i, p := range parts {
		widths[i] = float64(len([]rune(p))) * estCharWidthFactor * size
		total += widths[i]
	}
	total += space * float64(len(parts)-1)
	scale := 1.0
	if avail := fv.x1 - fv.x0; total > avail && avail > 0 {
		scale = avail / total
	}
	out := make([]Word, len(parts))
	x := fv.x0
	for i, p := range parts {
		out[i] = Word{X: x, W: widths[i] * scale, Text: p, Form: true}
		x += (widths[i] + space) * scale
	}
	return out
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
