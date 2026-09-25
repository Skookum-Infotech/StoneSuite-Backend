package docpdf

import "strings"

// defaultWording is the Terms & Conditions and Notes StoneSuite prints on a
// sales document whose record leaves them blank. It is stone-fabrication
// wording; a record's own text always wins.
type defaultWording struct{ terms, notes string }

// defaultWordingByKind holds the fallback per document kind. Kinds absent from
// the map (purchase orders, vendor documents) get no default: those are the
// buyer's documents, and the seller's payment terms do not apply to them.
var defaultWordingByKind = map[string]defaultWording{
	"ESTIMATE": {
		terms: "This estimate is valid for 30 days from the issue date. A 50% deposit is required to release slabs for fabrication, with the balance due before installation. Natural stone varies in color, veining and pattern; final material may differ slightly from samples. Pricing is based on the measurements and selections listed and may change if the scope changes.",
		notes: "Templates and final measurements are confirmed on site before cutting. Installation is scheduled after the deposit and approved shop drawings. Thank you for choosing us.",
	},
	"QUOTE": {
		terms: "This quote is valid for 30 days from the issue date. A 50% deposit is required to release slabs for fabrication, with the balance due before installation. Natural stone varies in color, veining and pattern; final material may differ slightly from samples.",
		notes: "Final measurements are confirmed on site before cutting. Thank you for the opportunity to quote your project.",
	},
	"SALES ORDER": {
		terms: "Production begins once the deposit is received. The balance is due before installation. Natural stone varies in color, veining and pattern. Changes after templating may affect price and lead time.",
		notes: "We will contact you to schedule templating and installation. Thank you for your order.",
	},
	"INVOICE": {
		terms: "Payment is due within 30 days of the invoice date. Balances past due may be subject to a late fee. Please include the invoice number with your payment.",
		notes: "Thank you for your business.",
	},
}

// withDefaultWording fills a blank Terms or Notes from the kind's default.
func withDefaultWording(d PrintableDoc) PrintableDoc {
	w, ok := defaultWordingByKind[strings.ToUpper(strings.TrimSpace(d.Kind))]
	if !ok {
		return d
	}
	if strings.TrimSpace(d.Terms) == "" {
		d.Terms = w.terms
	}
	if strings.TrimSpace(d.Notes) == "" {
		d.Notes = w.notes
	}
	return d
}
