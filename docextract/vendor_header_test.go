package docextract

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func vendorLetterhead() []prow {
	return rows(at(750, c(50, "Granite Works LLC")), at(738, c(50, "900 Quarry Rd")), at(726, c(50, "Tulsa, OK 74103")))
}

// Lessons 1, 2, 5: the vendor is the company that is not us; titles are never
// a party; a PO naming us as the vendor is a customer's order.
func TestPurchaseOrder_VendorResolution(t *testing.T) {
	tests := []struct {
		name       string
		doc        []byte
		wantVendor string
		wantWarn   []string
		notWarn    []string
	}{
		{
			name: "vendor quote: letterhead is the vendor, we are Quote To",
			doc: docPDF(vendorLetterhead(), rows(at(690, c(50, "QUOTATION")), at(670, c(50, "Quote #: Q-100")), at(640, c(50, "Quote To:"))),
				ownBlockRows(50, 624), tableRows(560, "Slab", "$10.00")),
			wantVendor: "Granite Works LLC", notWarn: []string{WarnCustomerPO},
		},
		{
			name: "own PO: our letterhead is skipped, Vendor block wins",
			doc: docPDF(ownBlockRows(50, 750), rows(at(690, c(50, "PURCHASE ORDER")), at(670, c(50, "PO Number: 77")), at(640, c(50, "Vendor:")),
				at(624, c(50, "Granite Works LLC")), at(610, c(50, "900 Quarry Rd"))), tableRows(560, "Slab", "$10.00")),
			wantVendor: "Granite Works LLC", notWarn: []string{WarnCustomerPO},
		},
		{
			name: "customer PO: we are in the Vendor block, letterhead is the customer",
			doc: docPDF(vendorLetterhead(), rows(at(690, c(50, "PURCHASE ORDER")), at(670, c(50, "PO Number: 4471")), at(640, c(50, "Vendor:")),
				at(624, c(50, ownName)), at(610, c(50, "2200 Foundry Road"))), tableRows(560, "Slab", "$10.00")),
			wantVendor: "", wantWarn: []string{WarnCustomerPO},
		},
		{
			name: "customer PO: we are the To block of a titled purchase order",
			doc: docPDF(vendorLetterhead(), rows(at(690, c(50, "PURCHASE ORDER")), at(670, c(50, "PO Number: 4471")), at(640, c(50, "To:")),
				at(624, c(50, ownName)), at(610, c(50, "2200 Foundry Road"))), tableRows(560, "Slab", "$10.00")),
			wantVendor: "", wantWarn: []string{WarnCustomerPO},
		},
		{
			name: "a quote To block that is us is not a customer PO",
			doc: docPDF(vendorLetterhead(), rows(at(690, c(50, "QUOTE")), at(670, c(50, "Quote #: Q-9")), at(640, c(50, "To:")),
				at(624, c(50, ownName)), at(610, c(50, "2200 Foundry Road"))), tableRows(560, "Slab", "$10.00")),
			wantVendor: "Granite Works LLC", notWarn: []string{WarnCustomerPO},
		},
		{
			name: "title row is never the vendor",
			doc: docPDF(rows(at(750, c(50, "PURCHASE ORDER")), at(738, c(50, "100 Main Street")), at(726, c(50, "Dallas, TX 75201")), at(700, c(50, "PO Number: 12"))),
				tableRows(560, "Slab", "$10.00")),
			wantVendor: "",
		},
		{
			name: "letterhead without an address line is not trusted",
			doc: docPDF(rows(at(750, c(50, "Some Heading")), at(738, c(50, "Notes follow")), at(700, c(50, "PO Number: 12"))),
				tableRows(560, "Slab", "$10.00")),
			wantVendor: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := extractDoc(t, tt.doc, DocTypePurchaseOrder, nil)
			assert.Equal(t, tt.wantVendor, res.Header.VendorName.Value)
			assert.False(t, res.Header.CustomerName.Found(), "a PO never names a customer")
			for _, w := range tt.wantWarn {
				assert.Contains(t, res.Warnings, w)
			}
			for _, w := range tt.notWarn {
				assert.NotContains(t, res.Warnings, w)
			}
			if tt.wantVendor != "" && res.Header.VendorName.Source == SourceDocument && res.Header.VendorName.Snippet == "Granite Works LLC" {
				assert.Equal(t, ConfCheck, res.Header.VendorName.Confidence, "a letterhead is inferred")
			}
		})
	}
}

func TestVendorBill_VendorResolution(t *testing.T) {
	tests := []struct {
		name       string
		doc        []byte
		wantVendor string
		wantWarn   string
	}{
		{
			name: "letterhead vendor",
			doc: docPDF(vendorLetterhead(), rows(at(690, c(50, "INVOICE")), at(670, c(50, "Invoice #: GW-1")), at(660, c(300, "Date: 01/05/2026")), at(640, c(50, "Bill To:"))),
				ownBlockRows(50, 624), tableRows(560, "Slab", "$10.00")),
			wantVendor: "Granite Works LLC",
		},
		{
			name: "Remit To box with a PO Box is not a company",
			doc: docPDF(rows(at(700, c(50, "Invoice #: GW-1")), at(680, c(50, "Remit To:")), at(666, c(50, "PO Box 4410")), at(652, c(50, "Houston, TX 77210"))),
				tableRows(560, "Slab", "$10.00")),
			wantVendor: "",
		},
		{
			name: "Remit To company is a last resort behind the letterhead",
			doc: docPDF(vendorLetterhead(), rows(at(700, c(50, "Invoice #: GW-1")), at(680, c(300, "Remit To:")), at(666, c(300, "Lockbox Services Inc"))),
				tableRows(560, "Slab", "$10.00")),
			wantVendor: "Granite Works LLC",
		},
		{
			name: "bill issued under our own name is a sales invoice",
			doc: docPDF(ownBlockRows(50, 750), rows(at(690, c(50, "INVOICE")), at(670, c(50, "Invoice #: ES-1")), at(640, c(50, "Bill To:")),
				at(624, c(50, "Acme Builders")), at(610, c(50, "5 Main St"))), tableRows(560, "Slab", "$10.00")),
			wantVendor: "", wantWarn: WarnDocLooksLike + classSalesInvoice,
		},
		{
			name:       "From block that is us",
			doc:        docPDF(rows(at(700, c(50, "Invoice #: ES-2")), at(680, c(50, "From:")), at(666, c(50, ownName))), tableRows(560, "Slab", "$10.00")),
			wantVendor: "", wantWarn: WarnDocLooksLike + classSalesInvoice,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := extractDoc(t, tt.doc, DocTypeVendorBill, nil)
			assert.Equal(t, tt.wantVendor, res.Header.VendorName.Value)
			assert.False(t, res.Header.CustomerName.Found())
			if tt.wantWarn != "" {
				assert.Contains(t, res.Warnings, tt.wantWarn)
			}
		})
	}
}

// Lesson 3: the invoice number under a boxed or letter-spaced title.
func TestVendorBill_InvoiceNumber(t *testing.T) {
	tests := []struct {
		name string
		top  []prow
		want string
	}{
		{"inline label", rows(at(700, c(50, "Invoice #: INV-0042"))), "INV-0042"},
		{"Invoice No.", rows(at(700, c(50, "Invoice No. 55120"))), "55120"},
		{"Bill number", rows(at(700, c(50, "Bill # B-901"))), "B-901"},
		{"label above value", rows(at(700, c(50, "Invoice #"), c(300, "Date")), at(686, c(50, "88123"), c(300, "01/05/2026"))), "88123"},
		{"letter-spaced title, number in a box under it", rows(at(750, c(50, "Granite Works LLC"), c(430, "I N V O I C E")), at(738, c(50, "900 Quarry Rd")), at(726, c(50, "Tulsa, OK 74103"), c(430, "SB-7702"))), "SB-7702"},
		{"plain title, number beneath", rows(at(750, c(400, "INVOICE")), at(736, c(400, "90031"))), "90031"},
		{"title never lends an address", rows(at(750, c(50, "INVOICE")), at(736, c(50, "4410 Commerce Parkway"))), ""},
		{"a date is never a number", rows(at(700, c(50, "Invoice # 01/05/2026"))), ""},
		{"no digit is no number", rows(at(700, c(50, "Invoice # PENDING"))), ""},
		{"vendor account number is not the invoice number", rows(at(700, c(50, "Account No. 4412"))), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := extractDoc(t, docPDF(tt.top, tableRows(560, "Slab", "$10.00")), DocTypeVendorBill, nil)
			assert.Equal(t, tt.want, res.Header.InvoiceNumber.Value)
			if tt.want == "" {
				assert.Contains(t, res.Warnings, WarnMissingInvoiceNumber)
				assert.Contains(t, res.Unresolved, KeyInvoiceNumber)
			}
		})
	}
}

func TestVendorBill_PORefAndDates(t *testing.T) {
	tests := []struct {
		name                    string
		top                     []prow
		po, invDate, due, terms string
		warns                   []string
	}{
		{"P.O.# is our PO", rows(at(700, c(50, "Invoice # 1"), c(300, "P.O.# EP-77"))), "EP-77", "", "", "", nil},
		{"Your PO", rows(at(700, c(50, "Invoice # 1"), c(300, "Your PO: EP-78"))), "EP-78", "", "", "", nil},
		{"Customer PO Number", rows(at(700, c(50, "Invoice # 1")), at(686, c(50, "Customer PO Number EP-79"))), "EP-79", "", "", "", nil},
		{"vendor Order No is not our PO", rows(at(700, c(50, "Invoice # 1"), c(300, "Order No. 5512"))), "", "", "", "", nil},
		{"two-digit year and US format", rows(at(700, c(50, "Invoice # 1"), c(300, "Invoice Date: 3/4/26"))), "", "2026-03-04", "", "", nil},
		{"plain Date is the invoice date", rows(at(700, c(50, "Invoice # 1"), c(300, "Date: 03/04/2026"))), "", "2026-03-04", "", "", nil},
		{"order date is not the invoice date", rows(at(700, c(50, "Invoice # 1"), c(300, "Order Date: 02/01/2026"))), "", "", "", "", []string{WarnMissingInvoiceDate}},
		{"Due Date maps to the due date, not delivery", rows(at(700, c(50, "Invoice Date: 03/04/2026"), c(300, "Due Date: 04/03/2026"))), "", "2026-03-04", "2026-04-03", "", nil},
		{"Payment Due", rows(at(700, c(50, "Invoice Date: 03/04/2026"), c(300, "Payment Due: 04/10/2026"))), "", "2026-03-04", "2026-04-10", "", nil},
		{"due from Net 30", rows(at(700, c(50, "Invoice Date: 03/04/2026"), c(300, "Terms: Net 30"))), "", "2026-03-04", "2026-04-03", "Net 30", []string{WarnDueFromTerms}},
		{"due from N days", rows(at(700, c(50, "Invoice Date: 03/04/2026"), c(300, "Terms: 45 days"))), "", "2026-03-04", "2026-04-18", "45 days", []string{WarnDueFromTerms}},
		{"explicit due wins over terms", rows(at(700, c(50, "Invoice Date: 03/04/2026"), c(200, "Terms: Net 30"), c(400, "Due Date: 05/01/2026"))), "", "2026-03-04", "2026-05-01", "Net 30", nil},
		{"due before invoice date is flagged", rows(at(700, c(50, "Invoice Date: 03/04/2026"), c(300, "Due Date: 02/01/2026"))), "", "2026-03-04", "2026-02-01", "", []string{WarnInvalidDate}},
		{"no invoice date means no derived due date", rows(at(700, c(50, "Terms: Net 30"))), "", "", "", "Net 30", []string{WarnMissingInvoiceDate}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := extractDoc(t, docPDF(tt.top, tableRows(560, "Slab", "$10.00")), DocTypeVendorBill, nil)
			assert.Equal(t, tt.po, res.Header.PONumber.Value, "po")
			assert.Equal(t, tt.invDate, res.Header.InvoiceDate.Value, "invoice date")
			assert.Equal(t, tt.due, res.Header.DueDate.Value, "due date")
			assert.Equal(t, tt.terms, res.Header.PaymentTerms.Value, "terms")
			assert.False(t, res.Header.DeliveryDate.Found(), "a bill has no delivery date")
			for _, w := range tt.warns {
				assert.Contains(t, res.Warnings, w)
			}
			if hasString(tt.warns, WarnDueFromTerms) {
				assert.Equal(t, SourceDefault, res.Header.DueDate.Source)
				assert.Equal(t, ConfCheck, res.Header.DueDate.Confidence)
			} else {
				assert.NotContains(t, res.Warnings, WarnDueFromTerms)
			}
		})
	}
}

func hasString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Lesson 7: revision wording and credits on a bill.
func TestVendorBill_NoRevisionAndCredit(t *testing.T) {
	top := rows(at(700, c(50, "Invoice # 5")), at(686, c(50, "Change Order 12 Revision 3 Amended")))
	res := extractDoc(t, docPDF(top, tableRows(560, "Slab", "$10.00")), DocTypeVendorBill, nil)
	assert.Nil(t, res.Revision)
	assert.Empty(t, res.QuoteRef)
	assert.NotContains(t, res.Warnings, WarnCreditDocument)

	credit := docPDF(rows(at(700, c(50, "Invoice # 6"))), tableRows(560, "Return", "-$10.00"),
		rows(at(500, c(440, "Subtotal"), pdftestRight(545, "-$10.00")), at(484, c(440, "Total"), pdftestRight(545, "-$10.00"))))
	res = extractDoc(t, credit, DocTypeVendorBill, nil)
	assert.Equal(t, "-10.00", res.Header.Total.Value)
	assert.Contains(t, res.Warnings, WarnCreditDocument)
}

// Lesson 6: totals; Balance Due never replaces the invoice total, charges.
func TestVendorBill_TotalsAndCharges(t *testing.T) {
	doc := docPDF(rows(at(700, c(50, "Invoice # 7"))),
		rows(
			at(600, c(50, "Item"), c(110, "Description"), pdftestRight(350, "Qty"), pdftestRight(460, "Unit Price"), pdftestRight(545, "Amount")),
			at(584, c(50, "A-1"), c(110, "Slab"), pdftestRight(350, "2"), pdftestRight(460, "$100.00"), pdftestRight(545, "$200.00")),
			at(568, c(110, "Freight"), pdftestRight(545, "$30.00")),
			at(552, c(110, "Fuel surcharge"), pdftestRight(545, "$5.00")),
			at(536, c(110, "Less deposit"), pdftestRight(545, "-$50.00")),
			at(500, c(440, "Subtotal"), pdftestRight(545, "$200.00")),
			at(484, c(440, "Invoice Total"), pdftestRight(545, "$235.00")),
			at(468, c(440, "Balance Due"), pdftestRight(545, "$185.00"))))
	res := extractDoc(t, doc, DocTypeVendorBill, nil)
	require.Len(t, res.Lines, 1, "freight, fuel and a deposit are never product lines")
	assert.Equal(t, "35.00", res.Header.Shipping.Value)
	assert.Equal(t, "235.00", res.Header.Total.Value)
}

func TestPurchaseOrder_NumbersAndDates(t *testing.T) {
	tests := []struct {
		name           string
		top            []prow
		class          string
		po, order, due string
	}{
		{"quote number on a quote", rows(at(740, c(50, "QUOTATION")), at(700, c(50, "Quote #: Q-100"), c(300, "Your PO: X-9"))), ClassQuote, "Q-100", "", ""},
		{"Quotation No", rows(at(740, c(50, "QUOTATION")), at(700, c(50, "Quotation No. 5521"))), ClassQuote, "5521", "", ""},
		{"Proforma No", rows(at(740, c(50, "PROFORMA INVOICE")), at(700, c(50, "Proforma No. PF-31"))), ClassQuote, "PF-31", "", ""},
		{"own PO number beats a vendor quote reference", rows(at(740, c(50, "PURCHASE ORDER")), at(700, c(50, "PO # EP-7"), c(300, "Quote # Q-3"))), ClassPurchaseOrder, "EP-7", "", ""},
		{"order and expected dates", rows(at(740, c(50, "PURCHASE ORDER")), at(700, c(50, "PO # EP-7"), c(250, "Order Date: 02/03/2026"), c(430, "Expected: 02/20/2026"))), ClassPurchaseOrder, "EP-7", "2026-02-03", ""},
		{"Due Date is the expected date", rows(at(740, c(50, "PURCHASE ORDER")), at(700, c(50, "PO # EP-7"), c(300, "Due Date: 02/20/2026"))), ClassPurchaseOrder, "EP-7", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := extractDoc(t, docPDF(tt.top, tableRows(560, "Slab", "$10.00")), DocTypePurchaseOrder, nil)
			assert.Equal(t, tt.class, res.ClassifiedAs)
			assert.Equal(t, tt.po, res.Header.PONumber.Value)
			assert.Equal(t, tt.order, res.Header.OrderDate.Value)
		})
	}
	res := extractDoc(t, docPDF(rows(at(740, c(50, "PURCHASE ORDER")), at(700, c(50, "PO # EP-7"), c(300, "Due Date: 02/20/2026"))), tableRows(560, "Slab", "$10.00")), DocTypePurchaseOrder, nil)
	assert.Equal(t, "2026-02-20", res.Header.DeliveryDate.Value)
	assert.False(t, res.Header.DueDate.Found())
}
