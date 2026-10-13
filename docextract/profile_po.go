package docextract

import "fmt"

// purchaseOrderProfile is the profile for a purchase order created from an
// upload: a vendor's quote or proforma, our own PO from another system, or a
// builder's countertop order form.
func purchaseOrderProfile() docProfile {
	return docProfile{
		build: buildPurchaseOrder, unresolved: purchaseOrderUnresolved, recommended: purchaseOrderRecommendedWarnings,
		llmKeys: purchaseOrderLLMKeys, notRecognized: purchaseOrderNotRecognized,
	}
}

// buildPurchaseOrder runs the deterministic stages over res.Pages.
func buildPurchaseOrder(res *Result, lim Limits, opts Options) error {
	if form, ok := parseOrderForm(res.Pages); ok {
		res.ClassifiedAs = ClassOrderForm
		res.Header = blankHeader()
		applyOrderFormAs(res, form, opts.OwnCompanyNames, true)
		if len(res.Lines) > lim.MaxLines {
			return newInputError(FailLineCap, fmt.Sprintf("%d lines exceeds the cap of %d", len(res.Lines), lim.MaxLines))
		}
		finishDoc(res, orderFormKeys(form), []string{ClassOrderForm}, purchaseOrderProfile())
		return nil
	}
	tbl, ho, err := buildVendorSide(res, lim, opts)
	if err != nil {
		return err
	}
	res.Revision = DetectRevision(res.Pages)
	// A vendor quote's own number is the document number; echoing it as a
	// quote reference would make the PO "convert from" itself.
	if q := DetectQuoteRef(res.Pages); q != res.Header.PONumber.Value {
		res.QuoteRef = q
	}
	finishDoc(res, ho.Keys, tbl.Roles, purchaseOrderProfile())
	return nil
}

// purchaseOrderUnresolved lists required purchase-order fields the parser
// could not resolve: the vendor and at least one product line.
func purchaseOrderUnresolved(r *Result) []string {
	var out []string
	if !r.Header.VendorName.Found() {
		out = append(out, KeyVendorName)
	}
	if productCount(r) == 0 {
		out = append(out, KeyLines)
	}
	return out
}

// purchaseOrderRecommendedWarnings warns about missing recommended fields.
func purchaseOrderRecommendedWarnings(r *Result) []string {
	var out []string
	if !r.Header.PONumber.Found() && r.ClassifiedAs != ClassOrderForm {
		out = append(out, WarnMissingPONumber)
	}
	if !r.Header.OrderDate.Found() && r.ClassifiedAs != ClassOrderForm {
		out = append(out, WarnMissingOrderDate)
	}
	return out
}

// purchaseOrderLLMKeys lists the header keys the model may be asked for.
func purchaseOrderLLMKeys(r *Result) []string {
	var out []string
	if !r.Header.VendorName.Found() {
		out = append(out, KeyVendorName)
	}
	if !r.Header.PONumber.Found() {
		out = append(out, KeyPONumber)
	}
	if !r.Header.OrderDate.Found() {
		out = append(out, KeyOrderDate)
	}
	return out
}

// purchaseOrderNotRecognized: no title class, no lines, no number and no vendor.
func purchaseOrderNotRecognized(res *Result) bool {
	return res.ClassifiedAs == "" && len(res.Lines) == 0 &&
		!res.Header.PONumber.Found() && !res.Header.VendorName.Found()
}
