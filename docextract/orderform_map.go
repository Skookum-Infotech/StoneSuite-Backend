package docextract

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Mapping a parsed countertop order form onto a sales-order Result. The form
// carries no prices, PO number or order date, so lines are qty 1 and unpriced
// (WarnNoPrices), and the job details go to Notes for the order memo.

const qtyOneMilli = 1000

// ofDetailLabels are the area values that describe a line beyond its color,
// in the order they read best.
var ofDetailLabels = []struct{ key, label string }{
	{ofFinish, "Finish"}, {ofEdge, "Edge"}, {ofCooktop, "Cooktop cutout"}, {ofFaucet, "Faucet spread"},
	{ofHalfWall, "Half wall"}, {ofPopUp, "Pop-up outlet"}, {ofLocation, "Location"}, {ofNotes, "Notes"},
}

// ofSKURe is a model number leading a sink value ("440185", "CRLR1813").
var ofSKURe = regexp.MustCompile(`^[A-Za-z0-9-]*\d[A-Za-z0-9-]*$`)

const ofMinSKULen = 4

// Free-mail domains say nothing about who the customer is.
var freeMailDomains = map[string]bool{
	"gmail": true, "yahoo": true, "outlook": true, "hotmail": true, "icloud": true, "aol": true,
	"live": true, "msn": true, "me": true, "comcast": true, "att": true, "sbcglobal": true, "proton": true, "protonmail": true,
}

// builderSuffixes split a run-together builder domain ("highlandhomes").
var builderSuffixes = []string{"homebuilders", "builders", "homes", "construction", "communities", "development", "companies", "company", "group"}

// applyOrderForm fills res from a parsed order form.
func applyOrderForm(res *Result, f orderForm, own []string) {
	h := &res.Header
	// No amounts on a form, so nothing to convert: the single-entity default.
	h.Currency = Field{Value: currencyUSD, Source: SourceDefault, Confidence: ConfHigh}
	if v, ok := f.Header[ofInstallDate]; ok {
		if iso, good := ParseDate(v.Text); good {
			h.DeliveryDate = ofField(iso, "Install Date "+v.Text, v, ConfHigh)
		} else {
			h.DeliveryDate = ofField(v.Text, "Install Date "+v.Text, v, ConfCheck)
			res.Warnings = append(res.Warnings, WarnInvalidDate)
		}
	}
	if addr, ok := f.Header[ofInstallAddress]; ok {
		ship := addr.Text
		if cz, ok := f.Header[ofCityZip]; ok {
			ship += "\n" + strings.Join(strings.Fields(strings.ReplaceAll(cz.Text, "/", " ")), " ")
		}
		h.ShipTo = ofField(ship, "Install Address "+addr.Text, addr, ConfHigh)
	}
	if email, ok := f.Header[ofEmail]; ok {
		if name := customerFromEmail(email.Text); name != "" && !isOwnCompany(name, own) {
			// Inferred from a domain, never printed: always for review.
			h.CustomerName = ofField(name, "Email "+email.Text, email, ConfCheck)
		}
	}
	if notes := orderFormNotes(f.Header); notes != "" {
		h.Notes = Field{Value: notes, Source: SourceDocument, Confidence: ConfHigh}
	}
	for _, a := range f.Areas {
		res.Lines = append(res.Lines, areaLines(a, len(res.Lines))...)
	}
	if len(res.Lines) > 0 {
		res.Warnings = append(res.Warnings, WarnNoPrices)
	}
}

// areaLines is one product line per filled area, plus its sink as an add-on.
// base is the number of lines already emitted (for the 1-based parent index).
func areaLines(a *ofArea, base int) []Line {
	if len(a.Values) == 0 {
		return nil
	}
	name := titleWords(a.Name)
	product := qtyOneLine(KindProduct)
	if c, ok := a.Values[ofColor]; ok {
		product.Description = ofField(c.Text, "Color "+c.Text, c, ConfHigh)
	} else {
		product.Description = Field{Value: name, Source: SourceDocument, Confidence: ConfCheck, Page: a.Page, Row: a.Row}
	}
	product.Detail = areaDetail(name, a.Values)
	out := []Line{product}
	if s, ok := a.Values[ofSink]; ok {
		sink := qtyOneLine(KindAddon)
		sink.ParentLine = base + 1
		sink.Description = ofField(s.Text, "Sink Model "+s.Text, s, ConfHigh)
		if tok := strings.Fields(s.Text)[0]; ofSKURe.MatchString(tok) && len(tok) >= ofMinSKULen {
			sink.SKU = ofField(tok, "Sink Model "+s.Text, s, ConfHigh)
		}
		sink.Detail = name + " sink"
		out = append(out, sink)
	}
	return out
}

// areaDetail is the area name and its specs: "Kitchen — Finish POLISHED; …".
func areaDetail(name string, v map[string]ofValue) string {
	var parts []string
	if bs := backsplash(v); bs != "" {
		parts = append(parts, bs)
	}
	for _, d := range ofDetailLabels {
		if x, ok := v[d.key]; ok {
			parts = append(parts, checkedOr(d.label, x.Text))
		}
	}
	if wf := waterfall(v); wf != "" {
		parts = append(parts, wf)
	}
	if len(parts) == 0 {
		return name
	}
	return name + " — " + strings.Join(parts, "; ")
}

// checkedOr renders a ticked box as its label alone, else "Label value".
func checkedOr(label, text string) string {
	if text == CheckedMark {
		return label
	}
	return label + " " + text
}

func backsplash(v map[string]ofValue) string {
	h, hasH := v[ofHeight]
	_, ticked := v[ofBacksplash]
	switch {
	case hasH:
		return "Backsplash " + h.Text
	case ticked:
		return "Backsplash"
	default:
		return ""
	}
}

func waterfall(v map[string]ofValue) string {
	q, hasQ := v[ofQty]
	_, ticked := v[ofWaterfall]
	switch {
	case hasQ:
		return "Waterfall qty " + q.Text
	case ticked:
		return "Waterfall"
	default:
		return ""
	}
}

// orderFormNotes is the job information for the order memo.
func orderFormNotes(hdr map[string]ofValue) string {
	get := func(k string) string { return hdr[k].Text }
	var lines []string
	if s := get(ofSpecial); s != "" {
		lines = append(lines, "Special instructions: "+s)
	}
	if contact := joinNonEmpty(", ", get(ofBuilder), get(ofPhone), get(ofEmail)); contact != "" {
		lines = append(lines, "Builder contact: "+contact)
	}
	var job []string
	for _, kv := range [][2]string{{"Community", get(ofCommunity)}, {"Plan", get(ofPlan)}, {"Measure date", get(ofMeasureDate)}} {
		if kv[1] != "" {
			job = append(job, kv[0]+": "+kv[1])
		}
	}
	if len(job) > 0 {
		lines = append(lines, strings.Join(job, ". ")+".")
	}
	return strings.Join(lines, "\n")
}

// customerFromEmail guesses the builder from a company email domain
// ("tyler@highlandhomes.com" → "Highland Homes"); "" for free mail.
func customerFromEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return ""
	}
	parts := strings.Split(strings.ToLower(email[at+1:]), ".")
	if len(parts) < 2 {
		return ""
	}
	label := parts[len(parts)-2]
	if freeMailDomains[label] || letters(label) != label {
		return ""
	}
	for _, suf := range builderSuffixes {
		if strings.HasSuffix(label, suf) && len(label) > len(suf) {
			return titleWords(strings.TrimSuffix(label, suf) + " " + suf)
		}
	}
	return titleWords(label)
}

func qtyOneLine(kind LineKind) Line {
	return Line{
		Kind: kind, SKU: notFound(), UoM: notFound(), UnitPrice: notFound(), Amount: notFound(),
		Qty:      Field{Value: "1", Source: SourceDefault, Confidence: ConfCheck},
		QtyMilli: qtyOneMilli,
	}
}

func ofField(value, snippet string, at ofValue, conf Confidence) Field {
	if len(snippet) > MaxSnippetLen {
		snippet = snippet[:MaxSnippetLen]
	}
	return Field{Value: value, Source: SourceDocument, Confidence: conf, Snippet: snippet, Page: at.Page, Row: at.Row}
}

// titleWords capitalises each word: "PRIMARY / MASTER BATH" → "Primary / Master Bath".
func titleWords(s string) string {
	ws := strings.Fields(strings.ToLower(s))
	for i, w := range ws {
		r, size := utf8.DecodeRuneInString(w)
		ws[i] = string(unicode.ToUpper(r)) + w[size:]
	}
	return strings.Join(ws, " ")
}

func joinNonEmpty(sep string, xs ...string) string {
	var out []string
	for _, x := range xs {
		if x != "" {
			out = append(out, x)
		}
	}
	return strings.Join(out, sep)
}
