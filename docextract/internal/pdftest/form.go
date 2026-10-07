package pdftest

import (
	"bytes"
	"fmt"
	"strings"
)

// Form field types (the PDF /FT names).
const (
	FieldText     = "Tx"
	FieldChoice   = "Ch"
	FieldCheckbox = "Btn"
)

const hiddenFlag = 2 // annotation /F bit 2: hidden

// Field is one filled AcroForm widget; its value lives in the widget, not in
// the page content, exactly as in a form filled in a PDF viewer. Rect is
// llx, lly, urx, ury. A checkbox uses Checked; Inherit puts /FT and /V on a
// parent field (how radio groups and renamed fields are stored). A choice
// field may list Options as [export, display] pairs and Selected indexes (/I).
type Field struct {
	Rect     [4]float64
	Type     string
	Value    string
	Checked  bool
	Hidden   bool
	Inherit  bool
	Options  [][2]string
	Selected []int
}

// Layer is text drawn inside a form XObject placed at (DX, DY), as an edited
// or imported page draws it; the plain page reader never sees it.
type Layer struct {
	DX, DY float64
	Texts  []Text
}

// formLayout is the AcroForm objects, numbered right after the page objects.
type formLayout struct {
	top     []int   // field ids listed in /AcroForm /Fields
	widgets [][]int // per page: widget annotation ids
	bodies  []string
}

func layoutFields(pages []Page) formLayout {
	f := formLayout{widgets: make([][]int, len(pages))}
	next := 4 + 2*len(pages)
	for pi, p := range pages {
		for _, fld := range p.Fields {
			name := fmt.Sprintf("f%d", next)
			value := pdfString(fld.Value)
			state := "/Off"
			if fld.Type == FieldCheckbox {
				if fld.Checked {
					state = "/Yes"
				}
				value = state
			}
			flags := 0
			if fld.Hidden {
				flags = hiddenFlag
			}
			rect := fmt.Sprintf("[%.2f %.2f %.2f %.2f]", fld.Rect[0], fld.Rect[1], fld.Rect[2], fld.Rect[3])
			extra := choiceKeys(fld)
			if fld.Inherit {
				parent, widget := next, next+1
				f.bodies = append(f.bodies,
					fmt.Sprintf("<< /T %s /FT /%s /V %s%s /Kids [%d 0 R] >>", pdfString(name), fld.Type, value, extra, widget),
					fmt.Sprintf("<< /Type /Annot /Subtype /Widget /Rect %s /F %d /Parent %d 0 R /AS %s >>", rect, flags, parent, state))
				f.top = append(f.top, parent)
				f.widgets[pi] = append(f.widgets[pi], widget)
				next += 2
				continue
			}
			f.bodies = append(f.bodies, fmt.Sprintf("<< /Type /Annot /Subtype /Widget /Rect %s /F %d /T %s /FT /%s /V %s%s /AS %s >>",
				rect, flags, pdfString(name), fld.Type, value, extra, state))
			f.top = append(f.top, next)
			f.widgets[pi] = append(f.widgets[pi], next)
			next++
		}
	}
	return f
}

// choiceKeys renders /Opt and /I for a choice field.
func choiceKeys(fld Field) string {
	var b strings.Builder
	if len(fld.Options) > 0 {
		b.WriteString(" /Opt [")
		for _, o := range fld.Options {
			fmt.Fprintf(&b, "[%s %s] ", pdfString(o[0]), pdfString(o[1]))
		}
		b.WriteString("]")
	}
	if len(fld.Selected) > 0 {
		b.WriteString(" /I [")
		for _, i := range fld.Selected {
			fmt.Fprintf(&b, "%d ", i)
		}
		b.WriteString("]")
	}
	return b.String()
}

// layerLayout is the form XObjects, numbered after the AcroForm objects.
type layerLayout struct {
	ids    [][]int // per page: XObject ids in draw order
	pages  []Page
	bodies []string
}

func layoutLayers(pages []Page, next int) layerLayout {
	l := layerLayout{ids: make([][]int, len(pages)), pages: pages}
	for pi, p := range pages {
		for _, layer := range p.Layers {
			stream := content(Page{Texts: layer.Texts})
			l.bodies = append(l.bodies, fmt.Sprintf(
				"<< /Type /XObject /Subtype /Form /BBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Length %d >>\nstream\n%s\nendstream",
				len(stream), stream))
			l.ids[pi] = append(l.ids[pi], next)
			next++
		}
	}
	return l
}

// resources is the page's /XObject resource entry ("" when it has none).
func (l layerLayout) resources(page int) string {
	if len(l.ids[page]) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(" /XObject <<")
	for i, id := range l.ids[page] {
		fmt.Fprintf(&b, " /L%d %d 0 R", i, id)
	}
	b.WriteString(" >>")
	return b.String()
}

// draw is the page content that places each layer.
func (l layerLayout) draw(page int) []byte {
	var b bytes.Buffer
	for i, layer := range l.pages[page].Layers {
		fmt.Fprintf(&b, "q 1 0 0 1 %.2f %.2f cm /L%d Do Q\n", layer.DX, layer.DY, i)
	}
	return b.Bytes()
}

func refs(ids []int) string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = fmt.Sprintf("%d 0 R", id)
	}
	return strings.Join(out, " ")
}

// pdfString is a literal PDF string with (, ) and \ escaped.
func pdfString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
	return "(" + r.Replace(s) + ")"
}
