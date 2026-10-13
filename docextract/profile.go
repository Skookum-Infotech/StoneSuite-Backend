package docextract

import "slices"

// docProfile is everything that differs between target document types: how to
// build a Result from parsed pages and which header fields are required,
// recommended and worth asking the model for.
type docProfile struct {
	build         func(*Result, Limits, Options) error
	unresolved    func(*Result) []string
	recommended   func(*Result) []string
	llmKeys       func(*Result) []string
	notRecognized func(*Result) bool
	// post re-derives computed fields (a due date from terms) once the model
	// has filled the fields they depend on; nil when there is nothing to do.
	post func(*Result)
}

// profileFor returns the profile for a target document type.
func profileFor(dt DocType) (docProfile, bool) {
	switch dt {
	case DocTypeSalesOrder:
		return salesOrderProfile(), true
	case DocTypePurchaseOrder:
		return purchaseOrderProfile(), true
	case DocTypeVendorBill:
		return vendorBillProfile(), true
	default:
		return docProfile{}, false
	}
}

func salesOrderProfile() docProfile {
	return docProfile{
		build: buildSalesOrder, unresolved: salesOrderUnresolved, recommended: salesOrderRecommendedWarnings,
		llmKeys: salesOrderLLMKeys, notRecognized: notRecognized,
	}
}

// notRecognized reports a document with no title class, no line table, no PO
// number and no customer: almost certainly not a purchase order at all.
func notRecognized(res *Result) bool {
	return res.ClassifiedAs == "" && len(res.Lines) == 0 &&
		!res.Header.PONumber.Found() && !res.Header.CustomerName.Found()
}

// llmAllowed gates the model: never for a document that isn't recognisably
// the target, never for an order form (no PO number or date by design, only an
// email-domain clue the model could only guess at) and, for purchase orders
// and bills, never when the title says it is a different kind of document
// (a statement or packing slip has nothing the model could fill honestly).
func llmAllowed(res *Result, p docProfile) bool {
	if p.notRecognized(res) || res.ClassifiedAs == ClassOrderForm {
		return false
	}
	return res.DocType == DocTypeSalesOrder || !ClassMismatch(res.DocType, res.ClassifiedAs)
}

// finishDoc runs the checks every layout shares: required and recommended
// fields, injection phrases and the layout fingerprint.
func finishDoc(res *Result, keys, roles []string, p docProfile) {
	res.Unresolved = p.unresolved(res)
	res.Warnings = append(res.Warnings, p.recommended(res)...)
	res.Injection = ScanInjection(fullText(res.Pages))
	if len(res.Injection) > 0 {
		res.Warnings = append(res.Warnings, WarnInjectionPhrases)
	}
	res.LayoutFingerprint = fingerprint(keys, roles)
}

// reconcileResult runs the arithmetic self-check over the parsed lines and
// remembers which rows to hide from the model's view of the document.
func reconcileResult(res *Result, tbl tableResult, opts Options) {
	rec := opts.ReconcileFn
	if rec == nil {
		rec = Reconcile
	}
	var amounts []Cents
	for _, l := range res.Lines {
		if l.Amount.Found() && !hasFlag(l.Flags, FlagAmountUnparsed) {
			amounts = append(amounts, l.AmountCents)
		}
	}
	h := res.Header
	res.Checks = rec(amounts, fieldCents(h.Subtotal), fieldCents(h.Tax), fieldCents(h.Shipping), fieldCents(h.Discount), fieldCents(h.Total))
	res.residualSpans = tbl.Spans
}

// addWarning appends a warning once.
func addWarning(res *Result, w string) {
	if !slices.Contains(res.Warnings, w) {
		res.Warnings = append(res.Warnings, w)
	}
}

// productCount counts the lines that are products.
func productCount(r *Result) int {
	n := 0
	for _, l := range r.Lines {
		if l.Kind == KindProduct {
			n++
		}
	}
	return n
}

// buildVendorSide runs the stages a purchase order and a vendor bill share:
// classify, parse the table, read the header with the vendor vocabulary, fold
// charge rows into the header and reconcile.
func buildVendorSide(res *Result, lim Limits, opts Options) (tableResult, headerOut, error) {
	res.ClassifiedAs = Classify(res.Pages)
	if ClassMismatch(res.DocType, res.ClassifiedAs) {
		res.Warnings = append(res.Warnings, WarnDocLooksLike+res.ClassifiedAs)
	}
	tbl, err := parseTable(res.Pages, lim)
	if err != nil {
		return tbl, headerOut{}, err
	}
	if !tbl.Found {
		res.Warnings = append(res.Warnings, WarnNoLineTable)
	}
	ho := parseHeader(res.Pages, tbl.Spans, HeaderOptions{OwnCompanyNames: opts.OwnCompanyNames, DocType: res.DocType, Class: res.ClassifiedAs})
	res.Header = ho.Header
	res.Warnings = append(res.Warnings, ho.Warnings...)
	res.Lines = tbl.Lines
	res.Warnings = append(res.Warnings, applyCharges(&res.Header, tbl.Charges)...)
	reconcileResult(res, tbl, opts)
	return tbl, ho, nil
}
