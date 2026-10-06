package docpdf

import "strings"

// withDefaultWording fills a blank Terms or Notes from the seller's own
// default for the document's kind (Seller.Defaults, set from the tenant's
// company profile). StoneSuite supplies no wording of its own: a tenant that
// configured none gets those sections omitted rather than boilerplate that
// is not theirs.
func withDefaultWording(d PrintableDoc) PrintableDoc {
	w, ok := d.Seller.Defaults[strings.ToUpper(strings.TrimSpace(d.Kind))]
	if !ok {
		return d
	}
	if strings.TrimSpace(d.Terms) == "" {
		d.Terms = w.Terms
	}
	if strings.TrimSpace(d.Notes) == "" {
		d.Notes = w.Notes
	}
	return d
}
