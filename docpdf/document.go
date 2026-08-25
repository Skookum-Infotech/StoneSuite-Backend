// Package docpdf renders normalized business documents (quotes, estimates,
// sales orders, invoices) to PDF using a pure-Go engine. It imports nothing
// app-specific — callers map their typed records into a PrintableDoc.
package docpdf

import (
	"bytes"
	"fmt"
)

// Seller is the letterhead block (the tenant's own identity).
type Seller struct {
	Name         string
	AddrLine1    string
	AddrLine2    string
	CityStateZip string
	Phone        string
	Email        string
	LogoPNG      []byte // reserved extension point; no logo store today
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

// Render produces PDF bytes for the document.
func Render(d PrintableDoc) ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	pdf := newDoc()
	drawHeader(pdf, d)
	drawParties(pdf, d)
	drawLineTable(pdf, d)
	drawTotals(pdf, d)
	drawFooter(pdf, d)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, fmt.Errorf("docpdf: output: %w", err)
	}
	if err := pdf.Error(); err != nil {
		return nil, fmt.Errorf("docpdf: render: %w", err)
	}
	return buf.Bytes(), nil
}

// money formats a monetary amount with the document's currency symbol.
func money(sym string, v float64) string {
	if sym == "" {
		sym = "$"
	}
	return fmt.Sprintf("%s%.2f", sym, v)
}
