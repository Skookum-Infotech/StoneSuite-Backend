package docextract

import (
	"regexp"
)

// A countertop order form (a builder's fillable spec sheet) is not a priced
// PO: a header of job details, then boxes per area (Kitchen, Island, Bath 2…),
// each with Color/Finish/Edge/Sink and so on. Values exist only as form words
// (see acroform.go), which is what makes this reliable: a printed word is a
// label or a heading, a form word is always a value, so a typed "CARRARO ORO"
// can never be read as a heading and printed hint text never as a value.

// Order-form label keys: the label's letters, lower-cased, with no spaces.
const (
	ofBuilder        = "builder"
	ofCommunity      = "community"
	ofPlan           = "plan"
	ofPhone          = "phone"
	ofInstallAddress = "installaddress"
	ofMeasureDate    = "measuredate"
	ofEmail          = "email"
	ofCityZip        = "cityzip"
	ofInstallDate    = "installdate"
	ofSpecial        = "specialinstructions"
	ofColor          = "color"
	ofFinish         = "finish"
	ofEdge           = "edge"
	ofBacksplash     = "backsplash"
	ofHeight         = "height"
	ofSink           = "sinkmodel"
	ofCooktop        = "cooktopmodel"
	ofWaterfall      = "waterfall"
	ofQty            = "qty"
	ofPopUp          = "popupoutlet"
	ofHalfWall       = "halfwall"
	ofFaucet         = "faucetspread"
	ofNotes          = "notes"
	ofLocation       = "location"
)

const (
	ofMaxLabelWords   = 4  // a label phrase spans at most this many words
	ofMinPrefixLabel  = 12 // only a label this long may match as a prefix ("Instructio*n*sF…")
	ofColumnSlack     = 6. // a word this far left of a column's heading still belongs to it
	ofColumnMerge     = 20 // headings this close in X start the same column
	ofMarkMaxDistance = 12 // a tick this far from a faucet-spread mark is not on it
	ofMinHeaderLabels = 3  // header labels needed to call it an order form
	ofHeadingGap      = 30 // a gap this wide ends a heading (the next column's starts)
)

var ofLabels = []string{
	ofBuilder, ofCommunity, ofPlan, ofPhone, ofInstallAddress, ofMeasureDate, ofEmail, ofCityZip,
	ofInstallDate, ofSpecial, ofColor, ofFinish, ofEdge, ofBacksplash, ofHeight, ofSink, ofCooktop,
	ofWaterfall, ofQty, ofPopUp, ofHalfWall, ofFaucet, ofNotes, ofLocation,
}

var ofHeaderLabels = map[string]bool{
	ofBuilder: true, ofCommunity: true, ofPlan: true, ofPhone: true, ofInstallAddress: true,
	ofMeasureDate: true, ofEmail: true, ofCityZip: true, ofInstallDate: true, ofSpecial: true,
}

// ofAreaWords start an area heading ("KITCHEN", "PRIMARY / MASTER BATH", "CUSTOM AREA:").
var ofAreaWords = map[string]bool{
	"kitchen": true, "island": true, "pantry": true, "hutch": true, "utility": true, "laundry": true,
	"primary": true, "master": true, "bath": true, "powder": true, "custom": true, "bar": true,
	"butler": true, "vanity": true, "outdoor": true, "fireplace": true, "desk": true, "office": true,
	"media": true, "mudroom": true, "guest": true, "secondary": true, "game": true, "wet": true,
}

// ofMarkRe is a faucet-spread position printed over a tick box (8", 4", C).
var ofMarkRe = regexp.MustCompile(`^(\d{1,2}"|C)$`)

// ofValue is one typed value and where it sits.
type ofValue struct {
	Text      string
	Page, Row int
}

// ofArea is one area box and its labelled values.
type ofArea struct {
	Name      string
	Page, Row int
	Values    map[string]ofValue
}

// orderForm is a parsed countertop order form.
type orderForm struct {
	Header map[string]ofValue
	Areas  []*ofArea
}

// ofColumn tracks one column of area boxes while rows are read.
type ofColumn struct {
	area  *ofArea
	label string // the label the next unlabelled value belongs to
	marks []Word // faucet-spread positions seen most recently
}

// parseOrderForm reads a filled countertop order form; ok is false when the
// pages are not one (too few header labels, no area box, or nothing typed).
func parseOrderForm(pages []PageRows) (orderForm, bool) {
	f := orderForm{Header: map[string]ofValue{}}
	typed := false
	for _, p := range pages {
		starts := columnStarts(p.Rows)
		cols := make([]*ofColumn, len(starts)+1)
		for i := range cols {
			cols[i] = &ofColumn{}
		}
		header := &ofColumn{}
		inAreas := false
		for ri, r := range p.Rows {
			if !inAreas && rowHasHeading(r) {
				inAreas = true
			}
			if !inAreas {
				typed = f.readSegment(header, r.Words, p.Page, ri) || typed
				continue
			}
			for ci, seg := range splitColumns(r.Words, starts) {
				typed = f.readSegment(cols[ci], seg, p.Page, ri) || typed
			}
		}
	}
	return f, typed && len(f.Areas) > 0 && len(f.Header) >= ofMinHeaderLabels
}

// readSegment reads one row's words within one column (or the header band):
// a heading opens an area, labels set where values go, form words are values.
// It reports whether any typed value was seen.
func (f *orderForm) readSegment(c *ofColumn, words []Word, page, row int) bool {
	// Ticks and the positions printed over them share a row in either order.
	var marks []Word
	for _, w := range words {
		if !w.Form && ofMarkRe.MatchString(w.Text) {
			marks = append(marks, w)
		}
	}
	if len(marks) > 0 {
		c.marks = marks
	}
	typed := false
	for i := 0; i < len(words); {
		if words[i].Form {
			j := i
			for j < len(words) && words[j].Form {
				j++
			}
			f.assign(c, words[i:j], page, row)
			typed, i = true, j
			continue
		}
		j := i
		for j < len(words) && !words[j].Form {
			j++
		}
		f.readStatic(c, words[i:j], page, row)
		i = j
	}
	return typed
}

// readStatic handles a run of printed words: an optional heading and labels.
// The header band only takes header labels and an area
// only area labels, so hint text ("…use the notes field…") can't relabel.
func (f *orderForm) readStatic(c *ofColumn, run []Word, page, row int) {
	if n := headingLen(run); n > 0 {
		c.area = &ofArea{Name: joinWords(run[:n]), Page: page, Row: row, Values: map[string]ofValue{}}
		c.label, c.marks = "", nil
		f.Areas = append(f.Areas, c.area)
		run = run[n:]
	}
	header := c.area == nil
	for i := 0; i < len(run); {
		if key, n := matchLabel(run[i:]); n > 0 && ofHeaderLabels[key] == header {
			c.label = key
			i += n
			continue
		}
		i++
	}
}

// assign stores typed words under the column's current label. Ticks under
// "Faucet Spread" become the positions printed over them.
func (f *orderForm) assign(c *ofColumn, vals []Word, page, row int) {
	text := joinWords(vals)
	switch {
	case c.label == "" && c.area != nil && c.area.Name != "" && len(c.area.Values) == 0:
		// A value right after the heading names a custom area.
		c.area.Name += " " + text
		return
	case c.label == ofFaucet:
		text = faucetPositions(vals, c.marks)
		if text == "" {
			return
		}
	}
	if c.label == "" {
		return
	}
	target := f.Header
	if c.area != nil {
		target = c.area.Values
	}
	if prev, ok := target[c.label]; ok && prev.Text != "" {
		prev.Text += " " + text
		target[c.label] = prev
		return
	}
	target[c.label] = ofValue{Text: text, Page: page, Row: row}
}
