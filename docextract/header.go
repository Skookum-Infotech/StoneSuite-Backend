package docextract

import (
	"regexp"
	"sort"
	"strings"
)

const (
	maxTermsLen     = 60
	currencyUSD     = "USD"
	termsRejectCond = "and conditions"
)

var (
	poTokenRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9\-/_.]*`)
	dateFindRe  = regexp.MustCompile(`\d{1,2}/\d{1,2}/\d{2,4}|\d{4}-\d{2}-\d{2}|[A-Za-z]{3,9}\.?\s+\d{1,2}(?:st|nd|rd|th)?,?\s+\d{4}`)
	moneyValRe  = regexp.MustCompile(`[-(]?\s?[$€£]\s?\d[\d.,]*\)?|[-(]?\d[\d.,]*[.,]\d{2}\b\)?`)
	netTermsRe  = regexp.MustCompile(`(?i)\bnet\s*\d{1,3}\b|\bdue\s+on\s+receipt\b|\bcod\b|\bprepaid\b`)
	currencyRe2 = regexp.MustCompile(`\b(EUR|GBP|CAD|AUD|JPY|CHF|MXN|INR|CNY|NZD)\b`)
	usdWordRe   = regexp.MustCompile(`\bUSD\b`)
	taxInclRe   = regexp.MustCompile(`(?i)\b(?:tax|vat|gst)\s+(?:inclusive|included)\b|\bincl(?:uding|\.)?\s+(?:tax|vat|gst)\b|\bprices?\s+include\s+(?:tax|vat|gst)\b`)
	symbolCodes = map[string]string{"€": "EUR", "£": "GBP", "¥": "JPY"}
)

// HeaderOptions tunes header extraction.
type HeaderOptions struct {
	OwnCompanyNames []string
}

// headerOut is the parsed header plus bookkeeping for the caller.
type headerOut struct {
	Header   Header
	Warnings []string
	Keys     []string // label keys seen, for the layout fingerprint
}

// parseHeader extracts the sales-order header from rows outside the line table.
func parseHeader(pages []PageRows, spans map[int][2]int, opts HeaderOptions) headerOut {
	var rows []hrow
	var fullText strings.Builder
	for _, p := range pages {
		sp, hasSpan := spans[p.Page]
		for i, r := range p.Rows {
			fullText.WriteString(r.Text())
			fullText.WriteByte('\n')
			if hasSpan && i >= sp[0] && i <= sp[1] {
				continue
			}
			if len(ScanInjection(r.Text())) > 0 {
				continue // instructions aimed at the model never feed a field
			}
			rows = append(rows, newHRow(p.Page, i, r))
		}
	}
	hits := findHits(rows)
	out := headerOut{}
	h := &out.Header
	h.PONumber = notFound()
	h.OrderDate, h.DeliveryDate = notFound(), notFound()
	h.CustomerName, h.BillTo, h.ShipTo = notFound(), notFound(), notFound()
	h.PaymentTerms = notFound()
	h.Subtotal, h.Tax, h.Shipping, h.Discount, h.Total = notFound(), notFound(), notFound(), notFound(), notFound()

	seen := map[string]bool{}
	for _, hit := range hits {
		seen[hit.spec.key] = true
	}
	for k := range seen {
		out.Keys = append(out.Keys, k)
	}
	sort.Strings(out.Keys)

	pos, multi := extractPO(rows, hits)
	h.PONumber = pos
	if multi {
		out.Warnings = append(out.Warnings, WarnMultipleDocuments)
	}
	var warn []string
	h.OrderDate, warn = extractDate(rows, hits, keyOrderDate)
	out.Warnings = append(out.Warnings, warn...)
	h.DeliveryDate, warn = extractDate(rows, hits, keyDelivery)
	out.Warnings = append(out.Warnings, warn...)
	h.CustomerName, h.BillTo = extractParty(rows, hits, keyBillTo, opts.OwnCompanyNames, true)
	_, h.ShipTo = extractParty(rows, hits, keyShipTo, opts.OwnCompanyNames, false)
	h.PaymentTerms = extractTerms(rows, hits)
	h.Subtotal = extractMoney(rows, hits, keySubtotal)
	h.Tax = extractMoney(rows, hits, keyTax)
	h.Shipping = extractMoney(rows, hits, keyShipping)
	h.Discount = extractMoney(rows, hits, keyDiscount)
	h.Total = extractMoney(rows, hits, keyTotal)
	h.Currency, warn = detectCurrency(fullText.String())
	out.Warnings = append(out.Warnings, warn...)
	if taxInclRe.MatchString(fullText.String()) {
		out.Warnings = append(out.Warnings, WarnTaxInclusive)
	}
	return out
}

// segments returns the candidate value texts for a hit: inline first, then below.
func segments(rows []hrow, h labelHit) []string {
	segs := []string{inlineSeg(rows, h)}
	if h.spec.below {
		segs = append(segs, belowSeg(rows, h))
	}
	return segs
}

// hitField builds a field located at a hit's row.
func hitField(rows []hrow, h labelHit, value string, conf Confidence) Field {
	r := rows[h.ri]
	return field(value, r.text, r.page, r.idx, conf)
}

// extractPO returns the PO number and whether several distinct numbers exist.
func extractPO(rows []hrow, hits []labelHit) (Field, bool) {
	best := notFound()
	distinct := map[string]bool{}
	for _, h := range hitsFor(hits, keyPO, false) {
		if supersedeRe.MatchString(rows[h.ri].text) {
			continue // "Supersedes PO 4400" names another document
		}
		for _, seg := range segments(rows, h) {
			tok := poTokenRe.FindString(seg)
			tok = strings.TrimRight(tok, ".-/_")
			if tok == "" || !hasDigit(tok) {
				continue
			}
			distinct[strings.ToUpper(tok)] = true
			if !best.Found() {
				best = hitField(rows, h, tok, ConfHigh)
			}
			break
		}
	}
	return best, len(distinct) > 1
}

// extractDate returns the best date for a key, ISO-normalised.
func extractDate(rows []hrow, hits []labelHit, key string) (Field, []string) {
	var invalid *Field
	for _, h := range hitsFor(hits, key, false) {
		for _, seg := range segments(rows, h) {
			for _, m := range dateFindRe.FindAllString(seg, -1) {
				if iso, ok := ParseDate(m); ok {
					return hitField(rows, h, iso, ConfHigh), nil
				}
				if invalid == nil {
					f := hitField(rows, h, m, ConfCheck)
					invalid = &f
				}
			}
		}
	}
	if invalid != nil {
		return *invalid, []string{WarnInvalidDate}
	}
	return notFound(), nil
}

// extractMoney returns the last money figure beside the best label.
func extractMoney(rows []hrow, hits []labelHit, key string) Field {
	for _, h := range hitsFor(hits, key, true) {
		for _, seg := range segments(rows, h) {
			if tok, ok := lastMoney(seg); ok {
				c, issue := ParseMoney(tok)
				if issue == IssueEUDecimal || issue == IssueInvalid {
					return hitField(rows, h, tok, ConfCheck)
				}
				conf := ConfHigh
				if issue != "" {
					conf = ConfCheck
				}
				return hitField(rows, h, FormatCents(c), conf)
			}
		}
	}
	return notFound()
}

// lastMoney finds the last money token not followed by a percent sign.
func lastMoney(s string) (string, bool) {
	locs := moneyValRe.FindAllStringIndex(s, -1)
	for i := len(locs) - 1; i >= 0; i-- {
		end := locs[i][1]
		if end < len(s) && s[end] == '%' {
			continue
		}
		return strings.TrimRight(strings.TrimSpace(s[locs[i][0]:end]), ".,"), true
	}
	return "", false
}

// extractTerms finds payment terms by label, falling back to "Net 30" style text.
func extractTerms(rows []hrow, hits []labelHit) Field {
	for _, h := range hitsFor(hits, keyTerms, false) {
		for _, seg := range segments(rows, h) {
			low := strings.ToLower(seg)
			if seg == "" || strings.HasPrefix(low, termsRejectCond) || strings.HasPrefix(low, "&") {
				continue
			}
			if len(seg) > maxTermsLen {
				seg = seg[:maxTermsLen]
			}
			return hitField(rows, h, seg, ConfHigh)
		}
	}
	for _, r := range rows {
		if m := netTermsRe.FindString(r.text); m != "" {
			return field(m, r.text, r.page, r.idx, ConfCheck)
		}
	}
	return notFound()
}

// detectCurrency defaults to USD and flags other currencies.
func detectCurrency(text string) (Field, []string) {
	for sym, code := range symbolCodes {
		if strings.Contains(text, sym) {
			return Field{Value: code, Source: SourceDocument, Confidence: ConfCheck, Snippet: sym}, []string{WarnNonUSDCurrency}
		}
	}
	if m := currencyRe2.FindString(text); m != "" {
		return Field{Value: m, Source: SourceDocument, Confidence: ConfCheck, Snippet: m}, []string{WarnNonUSDCurrency}
	}
	if usdWordRe.MatchString(text) {
		return Field{Value: currencyUSD, Source: SourceDocument, Confidence: ConfHigh, Snippet: currencyUSD}, nil
	}
	return Field{Value: currencyUSD, Source: SourceDefault, Confidence: ConfHigh}, nil
}
