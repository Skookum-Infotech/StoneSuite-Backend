// Package docpdf renders normalized business documents (quotes, estimates,
// sales orders, invoices) to PDF using a pure-Go engine. It imports nothing
// app-specific — callers map their typed records into a PrintableDoc.
package docpdf

import (
	"bytes"
	"fmt"
	"math"
)

// Seller is the letterhead block (the tenant's own identity).
type Seller struct {
	Name         string
	AddrLine1    string
	AddrLine2    string
	CityStateZip string
	Phone        string
	Email        string
	// LogoPNG is the tenant's own logo as PNG bytes (controllers'
	// decodeLogoAsPNG is the only writer). It is shown directly on the ink band
	// beside the StoneSuite logo (no plate -- see docpdf/brand.go); empty or
	// undecodable bytes leave the masthead with the StoneSuite logo alone.
	LogoPNG []byte
	// Payment is where the seller asks to be paid. It is printed only on
	// documents that set PrintableDoc.ShowPayment.
	Payment PaymentDetails
}

// PaymentDetails are the bank details a customer needs to pay the seller.
// Fields are free text (account formats differ by country); blank ones are
// left out of the document.
type PaymentDetails struct {
	BankName      string
	AccountNumber string
	RoutingNumber string // wire routing number
}

// Address is a bill-to / ship-to block.
type Address struct {
	Name         string
	Attention    string
	Line1        string
	Line2        string
	CityStateZip string
	Phone        string
	Email        string
}

// PrintLine is one line-item row.
type PrintLine struct {
	SKU             string
	Name            string
	Description     string
	UnitCode        string
	Quantity        float64
	UnitPrice       float64
	DiscountPercent float64
	TaxPercent      float64
	LineTotal       float64
}

// PrintableDoc is the module-agnostic input to Render.
type PrintableDoc struct {
	Seller Seller

	Kind      string // "INVOICE", "QUOTE", "ESTIMATE", "SALES ORDER"
	Number    string
	Status    string
	IssueDate string
	DueDate   string

	BillTo Address
	ShipTo Address

	Lines          []PrintLine
	CurrencySymbol string

	Subtotal       float64
	DiscountTotal  float64
	TaxTotal       float64
	ShippingCharge float64
	Adjustment     float64
	GrandTotal     float64
	AmountPaid     float64
	BalanceDue     float64
	ShowBalance    bool // when true, render Amount Paid / Balance Due rows

	// ShowPayment prints Seller.Payment in a Payment Details section. Set it
	// only on documents where the seller is the one being paid (invoice, quote,
	// estimate, sales order); a purchase order or vendor bill must not carry the
	// seller's own bank details.
	ShowPayment bool

	Terms string
	Notes string
	Memo  string
}

// Validate checks the minimum fields required to render a coherent document.
func (d PrintableDoc) Validate() error {
	if d.Kind == "" {
		return fmt.Errorf("docpdf: Kind is required")
	}
	if d.Number == "" {
		return fmt.Errorf("docpdf: Number is required")
	}
	return nil
}

// Render produces PDF bytes for the document: a StoneSuite masthead carrying
// the document kind and number (and the tenant's logo, when it has one), then
// the letterhead, parties, line table, summary and, where the document asks
// for it, the payment details.
func Render(d PrintableDoc) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	pdf := newDoc()
	d = toCP1252(pdf, withDefaultWording(d))
	label := d.Kind + " " + d.Number
	setFooter(pdf, label)
	pdf.SetTitle(label, false)
	pdf.SetCreator("StoneSuite", false)

	pdf.SetY(drawMasthead(pdf, d) + mastheadGap)
	drawHeader(pdf, d)
	drawParties(pdf, d)
	drawLineTable(pdf, d)
	drawSummary(pdf, d, paymentReserve(pdf, d))
	drawPayment(pdf, d)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("docpdf: output: %w", err)
	}
	if err := pdf.Error(); err != nil {
		return nil, fmt.Errorf("docpdf: render: %w", err)
	}
	return buf.Bytes(), nil
}

// money formats a monetary amount with the document's currency symbol,
// placing the minus sign before the symbol ("-$5.00", not "$-5.00") and never
// printing a negative zero.
func money(sym string, v float64) string {
	if sym == "" {
		sym = "$"
	}
	if math.Abs(v) < 0.005 {
		v = 0
	}
	if v < 0 {
		return fmt.Sprintf("-%s%.2f", sym, -v)
	}
	return fmt.Sprintf("%s%.2f", sym, v)
}
