package docextract

import (
	"regexp"
	"strings"
	"unicode"
)

var poBoxRe = regexp.MustCompile(`(?i)^p\.?\s?o\.?\s*box\b`)

// vendorParty is the outcome of looking for the company that is not us.
type vendorParty struct {
	name        Field
	customerDoc bool // a purchase order that names us as the vendor
	// salesInvoice: a bill that carries our own name as the issuer.
	salesInvoice bool
}

// vendorCand is one labelled block that may name the vendor.
type vendorCand struct {
	name Field
	own  bool
	kind string // "strong", "remit", "from", "to"
}

// vendorNameOK reports whether a block's first line can be a company name: it
// has letters, does not open with a digit (a street) or "PO Box" and is not a
// document title.
func vendorNameOK(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || unicode.IsDigit([]rune(s)[0]) || poBoxRe.MatchString(s) {
		return false
	}
	return letterheadName(s, nil)
}

// labelCandidates reads every vendor-ish label block, best first.
func labelCandidates(rows []hrow, hits []labelHit, own []string) []vendorCand {
	var out []vendorCand
	for _, key := range []string{keyVendor, keyRemit} {
		for _, h := range hitsFor(hits, key, false) {
			lines := partyLines(rows, hits, h)
			if len(lines) == 0 {
				continue
			}
			r := rows[h.ri]
			c := vendorCand{
				name: field(cleanName(lines[0]), r.text, r.page, r.idx, ConfHigh),
				own:  isOwnCompany(lines[0], own),
				kind: "strong",
			}
			switch {
			case key == keyRemit:
				c.kind = "remit"
			case h.spec.prio < prioStrong && strings.EqualFold(r.text[h.start:h.end], "to"):
				c.kind = "to"
			case h.spec.prio < prioStrong:
				c.kind = "from"
			}
			if !c.own && !vendorNameOK(lines[0]) {
				continue
			}
			out = append(out, c)
		}
	}
	return out
}

// topRowName is the company name at the start of the first header row: the
// words before the first wide gap.
func topRowName(rows []hrow) string {
	if len(rows) == 0 || len(rows[0].row.Words) == 0 {
		return ""
	}
	words := rows[0].row.Words
	end := 1
	for end < len(words) && words[end].X-(words[end-1].X+words[end-1].W) <= letterheadWordGap {
		end++
	}
	return cleanName(joinWords(words[:end]))
}

// firstOf returns the first non-own candidate of a kind.
func firstOf(cands []vendorCand, kind string) (Field, bool) {
	for _, c := range cands {
		if c.kind == kind && !c.own {
			return c.name, true
		}
	}
	return notFound(), false
}

func anyOwn(cands []vendorCand, kinds ...string) bool {
	for _, c := range cands {
		if !c.own {
			continue
		}
		for _, k := range kinds {
			if c.kind == k {
				return true
			}
		}
	}
	return false
}

// resolveVendor finds the company that is not us: a Vendor / Supplier / Sold
// By block, then the letterhead (check confidence, titles and our own name
// excluded), then a Remit To block, then a bare From / To block. Our own name
// is never the vendor. A purchase order that puts us in the vendor block is a
// customer's order to us, and a bill issued under our own name is a sales
// invoice: neither gets a vendor.
func resolveVendor(rows []hrow, hits []labelHit, opts HeaderOptions, allowLetterhead bool) vendorParty {
	own := opts.OwnCompanyNames
	vp := vendorParty{name: notFound()}
	cands := labelCandidates(rows, hits, own)
	topOwn := isOwnCompany(topRowName(rows), own)
	lhName, _ := extractLetterhead(rows, hits, own)
	_, bill := extractParty(rows, hits, keyBillTo, own, false)
	other := lhName.Found() || bill.Found()

	ownStrong := anyOwn(cands, "strong")
	if opts.DocType == DocTypeVendorBill {
		vp.salesInvoice = ownStrong || anyOwn(cands, "from") || topOwn
	} else {
		toIsUs := anyOwn(cands, "to") && opts.Class == ClassPurchaseOrder && other
		if ownStrong || toIsUs {
			vp.customerDoc = true
			return vp
		}
	}
	if vp.salesInvoice {
		return vp
	}
	if f, ok := firstOf(cands, "strong"); ok {
		vp.name = f
		return vp
	}
	if allowLetterhead && lhName.Found() {
		vp.name = lhName
		return vp
	}
	for _, kind := range []string{"remit", "from", "to"} {
		if f, ok := firstOf(cands, kind); ok {
			vp.name = f
			return vp
		}
	}
	return vp
}
