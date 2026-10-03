package docextract

import (
	"math"
	"regexp"
	"strings"
)

const (
	milliPerWhole       = 1000
	amountMismatchCents = 1
)

var (
	chargeRe       = regexp.MustCompile(`(?i)^(freight|shipping|ship(?:ping)?\s*(?:&|and)\s*handling|handling|fuel(?:\s+surcharge)?|delivery(?:\s+(?:fee|charge))?|discount|deposit|surcharge|misc(?:ellaneous)?\s+charge)\b`)
	addonVocabRe   = regexp.MustCompile(`(?i)\b(edge|edging|cutouts?|sinks?|sealer|install(?:ation)?|template|templating|polish|backsplash)\b`)
	addonPrefixRe  = regexp.MustCompile(`(?i)^(\+|w/|add\b|add-?on\b)`)
	trailingUnitRe = regexp.MustCompile(`^([\d.,\-]+)\s+([A-Za-z][A-Za-z0-9./²]*)$`)
)

// buildLine converts one data row's cells into a Line (Kind is set by the caller).
func buildLine(page, idx int, text string, cells map[colRole][]Word, cols []column, lim Limits) Line {
	ln := Line{
		SKU:         field(cellText(cells[roleSKU]), text, page, idx, ConfHigh),
		Description: field(cellText(cells[roleDesc]), text, page, idx, ConfHigh),
		UoM:         field(cellText(cells[roleUoM]), text, page, idx, ConfHigh),
	}
	if _, hasDesc := columnByRole(cols, roleDesc); !hasDesc && ln.Description.Value == "" {
		ln.Description = ln.SKU
	}
	qtyText := cellText(cells[roleQty])
	if m := trailingUnitRe.FindStringSubmatch(qtyText); m != nil {
		qtyText = m[1]
		if !ln.UoM.Found() {
			ln.UoM = field(m[2], text, page, idx, ConfHigh)
		}
	}
	parseQtyField(&ln, qtyText, text, page, idx, lim)
	parsePriceFields(&ln, cellText(cells[rolePrice]), cellText(cells[roleAmount]), text, page, idx)
	checkAmountMismatch(&ln)
	return ln
}

// parseQtyField fills the qty field and numeric value, flagging bad input.
func parseQtyField(ln *Line, qtyText, text string, page, idx int, lim Limits) {
	if qtyText == "" {
		ln.Qty = notFound()
		ln.Flags = append(ln.Flags, FlagQtyUnparsed)
		return
	}
	v, issue := ParseQty(qtyText)
	if issue == IssueEUDecimal {
		ln.Flags = append(ln.Flags, FlagEUDecimal)
	}
	if !NumberFlagsUsable(issue) {
		ln.Qty = field(qtyText, text, page, idx, ConfCheck)
		ln.Flags = append(ln.Flags, FlagQtyUnparsed)
		return
	}
	ln.QtyMilli = v
	conf := ConfHigh
	if v <= 0 || v > lim.MaxQty*milliPerWhole {
		ln.Flags = append(ln.Flags, FlagQtyRange)
		conf = ConfCheck
	}
	ln.Qty = field(FormatMilli(v), text, page, idx, conf)
}

// parsePriceFields fills unit price and amount.
func parsePriceFields(ln *Line, priceText, amountText, text string, page, idx int) {
	ln.UnitPrice = moneyField(ln, priceText, FlagPriceUnparsed, text, page, idx, &ln.UnitPriceCents)
	if amountText == "" {
		ln.Amount = notFound()
		ln.Flags = append(ln.Flags, FlagAmountMissing)
		return
	}
	ln.Amount = moneyField(ln, amountText, FlagAmountUnparsed, text, page, idx, &ln.AmountCents)
}

// moneyField parses a money cell into out, returning the Field.
func moneyField(ln *Line, s, unparsedFlag, text string, page, idx int, out *Cents) Field {
	if s == "" {
		return notFound()
	}
	c, issue := ParseMoney(s)
	switch issue {
	case IssueEUDecimal:
		ln.Flags = append(ln.Flags, FlagEUDecimal, unparsedFlag)
		return field(s, text, page, idx, ConfCheck)
	case IssueInvalid:
		ln.Flags = append(ln.Flags, unparsedFlag)
		return field(s, text, page, idx, ConfCheck)
	case IssuePrecision:
		ln.Flags = append(ln.Flags, FlagPricePrecision)
	}
	*out = c
	conf := ConfHigh
	if issue != "" {
		conf = ConfCheck
	}
	return field(FormatCents(c), text, page, idx, conf)
}

// checkAmountMismatch flags a line whose qty x price disagrees with its amount.
func checkAmountMismatch(ln *Line) {
	if !ln.Qty.Found() || !ln.UnitPrice.Found() || !ln.Amount.Found() {
		return
	}
	if hasFlag(ln.Flags, FlagQtyUnparsed) || hasFlag(ln.Flags, FlagPriceUnparsed) || hasFlag(ln.Flags, FlagAmountUnparsed) || hasFlag(ln.Flags, FlagPricePrecision) {
		return
	}
	want := lineAmount(ln.QtyMilli, ln.UnitPriceCents)
	if diff := math.Abs(float64(want - ln.AmountCents)); diff > amountMismatchCents {
		ln.Flags = append(ln.Flags, FlagAmountMismatch)
	}
	if ln.UnitPriceCents < 0 && !chargeRe.MatchString(ln.Description.Value) {
		ln.Flags = append(ln.Flags, FlagNegativePrice)
	}
}

// hasFlag reports whether flags contains f.
func hasFlag(flags []string, f string) bool {
	for _, x := range flags {
		if x == f {
			return true
		}
	}
	return false
}

// kindFor decides charge / addon / product for a numeric row.
func (tp *tableParser) kindFor(ln Line, cells map[colRole][]Word) LineKind {
	desc := strings.TrimSpace(ln.Description.Value)
	if !ln.SKU.Found() && chargeRe.MatchString(desc) {
		return KindCharge
	}
	if addonPrefixRe.MatchString(desc) || addonVocabRe.MatchString(desc) {
		return KindAddon
	}
	if dc, ok := columnByRole(tp.cols, roleDesc); ok && len(cells[roleDesc]) > 0 {
		if cells[roleDesc][0].X > dc.x0+addonIndent {
			return KindAddon
		}
	}
	return KindProduct
}
