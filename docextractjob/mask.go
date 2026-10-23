package docextractjob

import "stonesuite-backend/docextract"

// maskField masks only the snippet of a numeric/identifier field; Value is never touched.
func maskField(f *docextract.Field) {
	f.Snippet = docextract.MaskSensitive(f.Snippet)
}

// maskFreeTextField masks both the snippet and the value of a free-text field.
func maskFreeTextField(f *docextract.Field) {
	maskField(f)
	f.Value = docextract.MaskSensitive(f.Value)
}

// maskLineFields applies maskField to every field of a line.
func maskLineFields(l *docextract.Line) {
	for _, f := range []*docextract.Field{&l.SKU, &l.Description, &l.UoM, &l.Qty, &l.UnitPrice, &l.Amount} {
		maskField(f)
	}
	l.Detail = docextract.MaskSensitive(l.Detail)
}

// maskExtracted returns a copy of res with card, IBAN and routing/account
// numbers masked in display and free-text fields only. Numeric and identifier
// values are never changed, and the input's slices are not mutated.
func maskExtracted(res docextract.Result) docextract.Result {
	h := &res.Header
	for _, f := range []*docextract.Field{
		&h.PONumber, &h.OrderDate, &h.DeliveryDate, &h.CustomerName, &h.PaymentTerms, &h.Subtotal, &h.Tax,
		&h.Shipping, &h.Discount, &h.Total, &h.Currency, &h.VendorName, &h.InvoiceNumber, &h.InvoiceDate, &h.DueDate,
	} {
		maskField(f)
	}
	for _, f := range []*docextract.Field{&h.Notes, &h.BillTo, &h.ShipTo} {
		maskFreeTextField(f)
	}

	if res.Lines != nil {
		lines := make([]docextract.Line, len(res.Lines))
		copy(lines, res.Lines)
		for i := range lines {
			maskLineFields(&lines[i])
		}
		res.Lines = lines
	}

	if res.Pages != nil {
		pages := make([]docextract.PageRows, len(res.Pages))
		for pi, p := range res.Pages {
			pages[pi] = p
			if p.Rows == nil {
				continue
			}
			rows := make([]docextract.Row, len(p.Rows))
			for ri, r := range p.Rows {
				rows[ri] = r
				if r.Words == nil {
					continue
				}
				words := make([]docextract.Word, len(r.Words))
				for wi, w := range r.Words {
					w.Text = docextract.MaskSensitive(w.Text)
					words[wi] = w
				}
				rows[ri].Words = words
			}
			pages[pi].Rows = rows
		}
		res.Pages = pages
	}
	return res
}
