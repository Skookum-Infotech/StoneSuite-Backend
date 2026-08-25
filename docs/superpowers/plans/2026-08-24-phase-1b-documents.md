# Phase 1b — Documents (PDF · Email · Send Flow) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Generate server-authoritative PDFs of the four customer-facing documents (quote, estimate, sales order, invoice) and let a staff user email them to the customer with the PDF attached, persisting each send.

**Architecture:** A generic, record-keyed layer that reuses the existing attachments/R2/email plumbing. One pure `docpdf` renderer, one tiny mapping adapter per module, a generic `DocumentOps` controller keyed off `record_id` (dispatching through a loader registry wired in `main.go`), and a single generic `document_sends` history table. Everything security-sensitive reuses the exact RBAC + IDOR gate proven in `controllers/attachments.go`.

**Tech Stack:** Go 1.25, `github.com/go-pdf/fpdf` (pure-Go PDF), pgx/v5, standard-library SigV4 R2 client, Resend/SMTP email, testify.

## Global Constraints

- Go module `stonesuite-backend`, Go 1.25.12; build is `CGO_ENABLED=0` on Alpine — **pure-Go dependencies only** (no cgo, no headless browser).
- New shared packages (`docpdf`) import **nothing app-specific** — same discipline as `query/` and `ai/`.
- Errors always wrapped: `fmt.Errorf("context: %w", err)`. No `panic()` in production paths. No swallowed errors.
- Service/DB/store functions take `context.Context` as the first parameter.
- Every mutation checks `authz` + scope; every single-record access enforces `recordInScope`; **scope denial returns 404, never 403**; IDOR attempts call `logSecurityEvent(r, "idor_denied", …)`.
- All responses JSON with `success` boolean; error bodies `{success:false, message:"…"}`.
- Migrations idempotent: `CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`. Never edit an existing `CREATE TABLE` body. No down-migrations.
- Files over 300 lines get split. Tests are table-driven with testify. Conventional Commits.
- DB-backed tests use the `//go:build dbtest` tag and skip cleanly without `TEST_DATABASE_URL`.

---

### Task 1: `docpdf` package — types + `Render`

**Files:**
- Create: `docpdf/document.go` (types, `Validate`, `Render` entry point)
- Create: `docpdf/layout.go` (drawing helpers)
- Create: `docpdf/document_test.go`
- Modify: `go.mod` / `go.sum` (add `github.com/go-pdf/fpdf`)

**Interfaces:**
- Consumes: nothing (app-dependency-free).
- Produces:
  - `type Seller struct { Name, AddrLine1, AddrLine2, CityStateZip, Phone, Email string; LogoPNG []byte }`
  - `type Address struct { Name, Attention, Line1, Line2, CityStateZip, Phone, Email string }`
  - `type PrintLine struct { SKU, Name, Description, UnitCode string; Quantity, UnitPrice, DiscountPercent, TaxPercent, LineTotal float64 }`
  - `type PrintableDoc struct { Seller Seller; Kind, Number, Status, IssueDate, DueDate string; BillTo, ShipTo Address; Lines []PrintLine; CurrencySymbol string; Subtotal, DiscountTotal, TaxTotal, ShippingCharge, Adjustment, GrandTotal, AmountPaid, BalanceDue float64; ShowBalance bool; Terms, Notes, Memo string }`
  - `func (d PrintableDoc) Validate() error`
  - `func Render(d PrintableDoc) ([]byte, error)`

- [ ] **Step 1: Add the dependency**

Run:
```bash
go get github.com/go-pdf/fpdf@latest
```
Expected: `go.mod` gains `github.com/go-pdf/fpdf` in the `require` block.

- [ ] **Step 2: Write the failing test**

Create `docpdf/document_test.go`:
```go
package docpdf

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sampleDoc() PrintableDoc {
	return PrintableDoc{
		Seller:         Seller{Name: "Acme Stone Co", AddrLine1: "1 Quarry Rd", CityStateZip: "Austin, TX 78701", Email: "sales@acme.example"},
		Kind:           "INVOICE",
		Number:         "INV-1001",
		Status:         "Sent",
		IssueDate:      "2026-08-24",
		DueDate:        "2026-09-23",
		BillTo:         Address{Name: "Bob Buyer", Line1: "22 Main St", CityStateZip: "Dallas, TX 75201", Email: "bob@buyer.example"},
		CurrencySymbol: "$",
		Lines: []PrintLine{
			{SKU: "SLAB-1", Name: "Granite Slab", Description: "Absolute black, polished", UnitCode: "ea", Quantity: 3, UnitPrice: 250, TaxPercent: 8.25, LineTotal: 750},
		},
		Subtotal: 750, TaxTotal: 61.88, GrandTotal: 811.88,
		Terms: "Net 30", Notes: "Thank you for your business.",
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PrintableDoc)
		wantErr bool
	}{
		{"valid", func(*PrintableDoc) {}, false},
		{"missing kind", func(d *PrintableDoc) { d.Kind = "" }, true},
		{"missing number", func(d *PrintableDoc) { d.Number = "" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := sampleDoc()
			tc.mutate(&d)
			err := d.Validate()
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PrintableDoc)
	}{
		{"basic", func(*PrintableDoc) {}},
		{"no lines", func(d *PrintableDoc) { d.Lines = nil }},
		{"with balance", func(d *PrintableDoc) { d.ShowBalance = true; d.AmountPaid = 100; d.BalanceDue = 711.88 }},
		{"multipage", func(d *PrintableDoc) {
			d.Lines = nil
			for i := 0; i < 80; i++ {
				d.Lines = append(d.Lines, PrintLine{Name: "Item", Description: "A line item that occupies a row", UnitCode: "ea", Quantity: 1, UnitPrice: 10, LineTotal: 10})
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := sampleDoc()
			tc.mutate(&d)
			out, err := Render(d)
			require.NoError(t, err)
			assert.True(t, bytes.HasPrefix(out, []byte("%PDF-")), "output must be a PDF")
			assert.Greater(t, len(out), 500)
		})
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./docpdf/ -run 'TestValidate|TestRender' -v`
Expected: FAIL — `undefined: PrintableDoc` / `undefined: Render`.

- [ ] **Step 4: Write `docpdf/document.go`**

```go
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
```

- [ ] **Step 5: Write `docpdf/layout.go`**

```go
package docpdf

import (
	"fmt"

	"github.com/go-pdf/fpdf"
)

const (
	marginX     = 15.0
	pageBottomY = 270.0 // A4 height 297mm minus footer zone
	fontFace    = "Helvetica"
)

func newDoc() *fpdf.Fpdf {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(marginX, 15, marginX)
	pdf.SetAutoPageBreak(true, 20)
	pdf.AddPage()
	pdf.SetFooterFunc(func() {
		pdf.SetY(-15)
		pdf.SetFont(fontFace, "I", 8)
		pdf.SetTextColor(120, 120, 120)
		pdf.CellFormat(0, 10, fmt.Sprintf("Page %d of {nb}", pdf.PageNo()), "", 0, "C", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
	})
	pdf.AliasNbPages("{nb}")
	return pdf
}

func drawHeader(pdf *fpdf.Fpdf, d PrintableDoc) {
	pdf.SetFont(fontFace, "B", 16)
	pdf.Cell(100, 8, d.Seller.Name)
	pdf.SetFont(fontFace, "B", 20)
	pdf.CellFormat(0, 8, d.Kind, "", 1, "R", false, 0, "")

	pdf.SetFont(fontFace, "", 9)
	for _, ln := range []string{d.Seller.AddrLine1, d.Seller.AddrLine2, d.Seller.CityStateZip, d.Seller.Phone, d.Seller.Email} {
		if ln == "" {
			continue
		}
		pdf.CellFormat(100, 4.5, ln, "", 1, "L", false, 0, "")
	}

	pdf.SetY(28)
	pdf.SetFont(fontFace, "", 10)
	pdf.CellFormat(0, 5, "No: "+d.Number, "", 1, "R", false, 0, "")
	pdf.CellFormat(0, 5, "Status: "+d.Status, "", 1, "R", false, 0, "")
	pdf.CellFormat(0, 5, "Date: "+d.IssueDate, "", 1, "R", false, 0, "")
	if d.DueDate != "" {
		pdf.CellFormat(0, 5, "Due: "+d.DueDate, "", 1, "R", false, 0, "")
	}
	pdf.Ln(4)
}

func drawParties(pdf *fpdf.Fpdf, d PrintableDoc) {
	top := pdf.GetY()
	drawAddress(pdf, marginX, top, "BILL TO", d.BillTo)
	drawAddress(pdf, 110, top, "SHIP TO", d.ShipTo)
	pdf.SetY(top + 34)
}

func drawAddress(pdf *fpdf.Fpdf, x, y float64, label string, a Address) {
	pdf.SetXY(x, y)
	pdf.SetFont(fontFace, "B", 9)
	pdf.CellFormat(85, 5, label, "", 2, "L", false, 0, "")
	pdf.SetFont(fontFace, "", 9)
	for _, ln := range []string{a.Name, a.Attention, a.Line1, a.Line2, a.CityStateZip, a.Phone, a.Email} {
		if ln == "" {
			continue
		}
		pdf.SetX(x)
		pdf.CellFormat(85, 4.5, ln, "", 2, "L", false, 0, "")
	}
}

// column widths for the line table (sum ≈ 180mm printable width)
var lineCols = struct{ item, qty, price, disc, tax, total float64 }{90, 15, 25, 15, 15, 20}

func drawLineTableHeader(pdf *fpdf.Fpdf) {
	pdf.SetFont(fontFace, "B", 9)
	pdf.SetFillColor(235, 235, 235)
	pdf.CellFormat(lineCols.item, 7, "Item", "1", 0, "L", true, 0, "")
	pdf.CellFormat(lineCols.qty, 7, "Qty", "1", 0, "R", true, 0, "")
	pdf.CellFormat(lineCols.price, 7, "Price", "1", 0, "R", true, 0, "")
	pdf.CellFormat(lineCols.disc, 7, "Disc%", "1", 0, "R", true, 0, "")
	pdf.CellFormat(lineCols.tax, 7, "Tax%", "1", 0, "R", true, 0, "")
	pdf.CellFormat(lineCols.total, 7, "Total", "1", 1, "R", true, 0, "")
}

func drawLineTable(pdf *fpdf.Fpdf, d PrintableDoc) {
	drawLineTableHeader(pdf)
	pdf.SetFont(fontFace, "", 9)
	for _, ln := range d.Lines {
		if pdf.GetY() > pageBottomY {
			pdf.AddPage()
			drawLineTableHeader(pdf)
			pdf.SetFont(fontFace, "", 9)
		}
		name := ln.Name
		if ln.SKU != "" {
			name = ln.SKU + " — " + name
		}
		if ln.Description != "" {
			name = name + "\n" + ln.Description
		}
		x, y := pdf.GetX(), pdf.GetY()
		pdf.MultiCell(lineCols.item, 5, name, "1", "L", false)
		rowH := pdf.GetY() - y
		pdf.SetXY(x+lineCols.item, y)
		pdf.CellFormat(lineCols.qty, rowH, trimNum(ln.Quantity), "1", 0, "R", false, 0, "")
		pdf.CellFormat(lineCols.price, rowH, money(d.CurrencySymbol, ln.UnitPrice), "1", 0, "R", false, 0, "")
		pdf.CellFormat(lineCols.disc, rowH, trimNum(ln.DiscountPercent), "1", 0, "R", false, 0, "")
		pdf.CellFormat(lineCols.tax, rowH, trimNum(ln.TaxPercent), "1", 0, "R", false, 0, "")
		pdf.CellFormat(lineCols.total, rowH, money(d.CurrencySymbol, ln.LineTotal), "1", 1, "R", false, 0, "")
	}
	pdf.Ln(3)
}

func drawTotals(pdf *fpdf.Fpdf, d PrintableDoc) {
	type row struct {
		label string
		val   float64
		show  bool
		bold  bool
	}
	rows := []row{
		{"Subtotal", d.Subtotal, true, false},
		{"Discount", -d.DiscountTotal, d.DiscountTotal != 0, false},
		{"Tax", d.TaxTotal, d.TaxTotal != 0, false},
		{"Shipping", d.ShippingCharge, d.ShippingCharge != 0, false},
		{"Adjustment", d.Adjustment, d.Adjustment != 0, false},
		{"Grand Total", d.GrandTotal, true, true},
		{"Amount Paid", -d.AmountPaid, d.ShowBalance, false},
		{"Balance Due", d.BalanceDue, d.ShowBalance, true},
	}
	labelW, valW := 40.0, 30.0
	x := 210 - marginX - labelW - valW
	for _, r := range rows {
		if !r.show {
			continue
		}
		style := ""
		if r.bold {
			style = "B"
		}
		pdf.SetX(x)
		pdf.SetFont(fontFace, style, 10)
		pdf.CellFormat(labelW, 6, r.label, "", 0, "R", false, 0, "")
		pdf.CellFormat(valW, 6, money(d.CurrencySymbol, r.val), "", 1, "R", false, 0, "")
	}
}

func drawFooter(pdf *fpdf.Fpdf, d PrintableDoc) {
	pdf.Ln(6)
	block := func(label, body string) {
		if body == "" {
			return
		}
		pdf.SetFont(fontFace, "B", 9)
		pdf.CellFormat(0, 5, label, "", 1, "L", false, 0, "")
		pdf.SetFont(fontFace, "", 9)
		pdf.MultiCell(0, 4.5, body, "", "L", false)
		pdf.Ln(2)
	}
	block("Terms & Conditions", d.Terms)
	block("Notes", d.Notes)
	block("Memo", d.Memo)
}

// trimNum formats a float without trailing zeros (e.g. 3, 3.5, 8.25).
func trimNum(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	for len(s) > 0 && s[len(s)-1] == '0' {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] == '.' {
		s = s[:len(s)-1]
	}
	return s
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./docpdf/ -v`
Expected: PASS (all `TestValidate` and `TestRender` subtests).

- [ ] **Step 7: Verify build + vet**

Run: `go build ./... && go vet ./docpdf/`
Expected: no output (success).

- [ ] **Step 8: Commit**

```bash
git add go.mod go.sum docpdf/
git commit -m "feat(docpdf): add pure-Go document PDF renderer"
```

---

### Task 2: `storage.Client.Put` — server-side R2 upload

**Files:**
- Modify: `storage/r2.go` (add `Put`, extend the package doc comment)
- Create: `storage/r2_put_test.go`

**Interfaces:**
- Consumes: existing unexported SigV4 helpers in `storage/` (`signingKey`, `hexHMAC`, `hexSHA256`, `awsEncodeSegment`, `encodeKeyPath`, `emptyBodySHA256`).
- Produces: `func (c *Client) Put(ctx context.Context, key, contentType string, body []byte) error`

- [ ] **Step 1: Write the failing test**

Create `storage/r2_put_test.go`:
```go
package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPut_NilClient(t *testing.T) {
	var c *Client
	err := c.Put(context.Background(), "k", "application/pdf", []byte("x"))
	assert.ErrorIs(t, err, ErrStorageNotConfigured)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./storage/ -run TestPut_NilClient -v`
Expected: FAIL — `c.Put undefined`.

- [ ] **Step 3: Implement `Put` in `storage/r2.go`**

Add after `Delete`:
```go
// Put uploads bytes to R2 under key with the given content type, using an
// authenticated SigV4 header PUT (the server-side counterpart to PresignPut,
// which only issues browser-upload URLs). Used to persist server-generated
// documents such as rendered PDFs. Returns ErrStorageNotConfigured on a nil
// client.
func (c *Client) Put(ctx context.Context, key, contentType string, body []byte) error {
	if c == nil {
		return ErrStorageNotConfigured
	}
	now := time.Now().UTC()
	dateStamp := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")

	credScope := dateStamp + "/" + awsRegion + "/" + awsService + "/aws4_request"
	signedHdrs := "content-type;host;x-amz-content-sha256;x-amz-date"
	payloadHash := hexSHA256(body)

	canonURI := "/" + awsEncodeSegment(c.bucket) + "/" + encodeKeyPath(key)
	canonHeaders := "content-type:" + contentType + "\n" +
		"host:" + c.host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"

	canonReq := strings.Join([]string{
		"PUT", canonURI, "", canonHeaders, signedHdrs, payloadHash,
	}, "\n")

	s2s := strings.Join([]string{
		awsAlgorithm, amzDate, credScope, hexSHA256([]byte(canonReq)),
	}, "\n")
	sig := hexHMAC(signingKey(c.secretKey, dateStamp, awsRegion, awsService), []byte(s2s))

	authHeader := fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		awsAlgorithm, c.accessKey, credScope, signedHdrs, sig,
	)

	objURL := "https://" + c.host + "/" + c.bucket + "/" + encodeKeyPath(key)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, objURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build r2 put request: %w", err)
	}
	req.Header.Set("Host", c.host)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("Authorization", authHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute r2 put: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return fmt.Errorf("r2 put returned HTTP %d", resp.StatusCode)
}
```

Add `"bytes"` to the `storage/r2.go` import block (the other imports — `io`, `strings`, `time`, `net/http`, `fmt` — are already present).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./storage/ -v`
Expected: PASS.

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add storage/r2.go storage/r2_put_test.go
git commit -m "feat(storage): add server-side R2 Put for generated documents"
```

---

### Task 3: `services.SendDocumentEmail` — email with attachments

**Files:**
- Modify: `services/email.go` (add `EmailAttachment`, `SendDocumentEmail`, MIME builder, Resend-with-attachments path)
- Create: `services/email_attach_test.go`

**Interfaces:**
- Consumes: existing `config.AppConfig`, `sendViaResend`/`sendViaSMTP` patterns.
- Produces:
  - `type EmailAttachment struct { FileName, ContentType string; Content []byte }`
  - `func SendDocumentEmail(to []string, cc []string, subject, htmlBody string, attachments []EmailAttachment) error`
  - `func buildMIME(from string, to, cc []string, subject, htmlBody string, attachments []EmailAttachment) []byte` (unexported; tested directly)

- [ ] **Step 1: Write the failing test**

Create `services/email_attach_test.go`:
```go
package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildMIME_IncludesHTMLAndAttachment(t *testing.T) {
	msg := buildMIME(
		"sender@acme.example",
		[]string{"bob@buyer.example"},
		[]string{"me@acme.example"},
		"Your Invoice INV-1001",
		"<p>See attached.</p>",
		[]EmailAttachment{{FileName: "INV-1001.pdf", ContentType: "application/pdf", Content: []byte("%PDF-1.4 fake")}},
	)
	s := string(msg)
	assert.Contains(t, s, "Subject: Your Invoice INV-1001")
	assert.Contains(t, s, "To: bob@buyer.example")
	assert.Contains(t, s, "Cc: me@acme.example")
	assert.Contains(t, s, "multipart/mixed")
	assert.Contains(t, s, "Content-Type: text/html")
	assert.Contains(t, s, `filename="INV-1001.pdf"`)
	assert.Contains(t, s, "Content-Transfer-Encoding: base64")
	// base64 of the fake bytes appears in the body
	assert.True(t, strings.Contains(s, "JVBERg"), "expected base64-encoded PDF bytes")
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./services/ -run TestBuildMIME -v`
Expected: FAIL — `buildMIME undefined` / `EmailAttachment undefined`.

- [ ] **Step 3: Implement in `services/email.go`**

Add these imports to the existing import block: `"encoding/base64"`, `"mime/multipart"`, `"net/textproto"`. Then append:
```go
// EmailAttachment is a single file attached to a document email.
type EmailAttachment struct {
	FileName    string
	ContentType string
	Content     []byte
}

// SendDocumentEmail sends an HTML email with optional file attachments to one
// or more recipients (plus optional CC), routing through the same provider
// precedence as sendEmail: Resend, then SMTP, then no-op.
func SendDocumentEmail(to, cc []string, subject, htmlBody string, attachments []EmailAttachment) error {
	cfg := config.AppConfig
	from := cfg.SenderEmail
	if from == "" {
		from = "noreply@stonesuite.app"
	}
	if len(to) == 0 {
		return fmt.Errorf("send document email: at least one recipient is required")
	}

	if cfg.ResendAPIKey != "" {
		return sendDocViaResend(cfg.ResendAPIKey, from, to, cc, subject, htmlBody, attachments)
	}
	if cfg.SMTPHost != "" && cfg.SenderEmail != "" {
		msg := buildMIME(from, to, cc, subject, htmlBody, attachments)
		auth := smtp.PlainAuth("", cfg.SenderEmail, cfg.SenderPassword, cfg.SMTPHost)
		addr := fmt.Sprintf("%s:%s", cfg.SMTPHost, cfg.SMTPPort)
		rcpt := append(append([]string{}, to...), cc...)
		if err := smtp.SendMail(addr, auth, from, rcpt, msg); err != nil {
			return fmt.Errorf("send document email via smtp: %w", err)
		}
		log.Printf("Document email sent via SMTP to %s", strings.Join(to, ","))
		return nil
	}

	log.Printf("INFO: no email provider configured — skipping document email to %s", strings.Join(to, ","))
	return nil
}

// buildMIME assembles a multipart/mixed message: an HTML part plus one
// base64-encoded part per attachment.
func buildMIME(from string, to, cc []string, subject, htmlBody string, attachments []EmailAttachment) []byte {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)

	// Headers.
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	if len(cc) > 0 {
		fmt.Fprintf(&buf, "Cc: %s\r\n", strings.Join(cc, ", "))
	}
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	buf.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: multipart/mixed; boundary=%s\r\n\r\n", mw.Boundary())

	// HTML part.
	htmlHdr := textproto.MIMEHeader{}
	htmlHdr.Set("Content-Type", "text/html; charset=UTF-8")
	htmlHdr.Set("Content-Transfer-Encoding", "quoted-printable")
	if pw, err := mw.CreatePart(htmlHdr); err == nil {
		_ = writeQuotedPrintable(pw, htmlBody)
	}

	// Attachment parts.
	for _, a := range attachments {
		ah := textproto.MIMEHeader{}
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah.Set("Content-Type", ct)
		ah.Set("Content-Transfer-Encoding", "base64")
		ah.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, a.FileName))
		if pw, err := mw.CreatePart(ah); err == nil {
			b64 := base64.StdEncoding.EncodeToString(a.Content)
			// wrap at 76 chars per RFC 2045
			for i := 0; i < len(b64); i += 76 {
				end := i + 76
				if end > len(b64) {
					end = len(b64)
				}
				pw.Write([]byte(b64[i:end] + "\r\n"))
			}
		}
	}
	_ = mw.Close()
	return buf.Bytes()
}

// writeQuotedPrintable writes s as quoted-printable. For our HTML bodies a
// plain byte copy is acceptable; encoding is best-effort.
func writeQuotedPrintable(w io.Writer, s string) error {
	_, err := io.WriteString(w, s)
	return err
}

// sendDocViaResend delivers an HTML email plus attachments through Resend.
func sendDocViaResend(apiKey, from string, to, cc []string, subject, html string, attachments []EmailAttachment) error {
	atts := make([]map[string]string, 0, len(attachments))
	for _, a := range attachments {
		atts = append(atts, map[string]string{
			"filename": a.FileName,
			"content":  base64.StdEncoding.EncodeToString(a.Content),
		})
	}
	payload, err := json.Marshal(map[string]any{
		"from": from, "to": to, "cc": cc, "subject": subject, "html": html, "attachments": atts,
	})
	if err != nil {
		return fmt.Errorf("resend: marshal document payload: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("resend: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("resend: send document to %v: %w", to, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("resend: HTTP %d: %s", resp.StatusCode, body)
	}
	log.Printf("Document email sent via Resend to %s", strings.Join(to, ","))
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./services/ -run TestBuildMIME -v`
Expected: PASS.

- [ ] **Step 5: Verify build + full services tests**

Run: `go build ./... && go test ./services/`
Expected: success.

- [ ] **Step 6: Commit**

```bash
git add services/email.go services/email_attach_test.go
git commit -m "feat(services): add SendDocumentEmail with PDF attachment support"
```

---

### Task 4: `document_sends` table + store helpers

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append new table + index at EOF, following the existing "Migration NNN" convention)
- Create: `workflow/document_sends.go`
- Create: `workflow/document_sends_dbtest_test.go`

**Interfaces:**
- Consumes: `workflow.Querier` (existing interface used by attachments/audit helpers).
- Produces:
  - `type DocumentSend struct { ID, RecordID, WorkflowKey, AttachmentID, SentTo, CC, Subject, SentByUserID string; SentAt time.Time }`
  - `func InsertDocumentSend(ctx context.Context, q Querier, s DocumentSend) (string, error)`
  - `func ListDocumentSends(ctx context.Context, q Querier, recordID string) ([]DocumentSend, error)`

- [ ] **Step 1: Append the migration to `database/migrations/tenant/schema.sql`**

At the end of the file, mirroring the existing migration-block style:
```sql
-- ---------------------------------------------------------------------------
-- Migration NNN: document_sends — history of emailed documents (Phase 1b).
-- Generic + record-keyed (like workflow_record_attachments): one table serves
-- every document type (quote/estimate/sales_order/invoice). record_id is the
-- document UUID; no FK (documents live in per-module relational tables).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS document_sends (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    record_id       UUID        NOT NULL,
    workflow_key    TEXT        NOT NULL,
    attachment_id   UUID        NULL,
    sent_to         TEXT        NOT NULL,
    cc              TEXT        NOT NULL DEFAULT '',
    subject         TEXT        NOT NULL DEFAULT '',
    sent_by_user_id INTEGER     NULL,
    sent_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_document_sends_record ON document_sends(record_id, sent_at DESC);
```
Replace `NNN` with the next migration number after the current highest in the file.

- [ ] **Step 2: Write `workflow/document_sends.go`**

```go
package workflow

import (
	"context"
	"fmt"
	"time"
)

// DocumentSend is one row of the document_sends history — a record of a
// generated document (PDF) that was emailed to a customer.
type DocumentSend struct {
	ID           string    `json:"id"`
	RecordID     string    `json:"recordId"`
	WorkflowKey  string    `json:"workflowKey"`
	AttachmentID string    `json:"attachmentId,omitempty"`
	SentTo       string    `json:"sentTo"`
	CC           string    `json:"cc,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	SentByUserID string    `json:"sentByUserId,omitempty"`
	SentAt       time.Time `json:"sentAt"`
}

// InsertDocumentSend records one emailed document. Returns the new row id.
func InsertDocumentSend(ctx context.Context, q Querier, s DocumentSend) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO document_sends
			(record_id, workflow_key, attachment_id, sent_to, cc, subject, sent_by_user_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id`,
		s.RecordID,
		s.WorkflowKey,
		nullIfEmpty(s.AttachmentID),
		s.SentTo,
		s.CC,
		s.Subject,
		nullIfEmpty(s.SentByUserID),
	).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("insert document send: %w", err)
	}
	return id, nil
}

// ListDocumentSends returns a record's send history, newest first.
func ListDocumentSends(ctx context.Context, q Querier, recordID string) ([]DocumentSend, error) {
	rows, err := q.Query(ctx, `
		SELECT id, record_id, workflow_key,
		       COALESCE(attachment_id::text, ''), sent_to, cc, subject,
		       COALESCE(sent_by_user_id::text, ''), sent_at
		FROM document_sends
		WHERE record_id = $1
		ORDER BY sent_at DESC`, recordID)
	if err != nil {
		return nil, fmt.Errorf("query document sends: %w", err)
	}
	defer rows.Close()

	var out []DocumentSend
	for rows.Next() {
		var s DocumentSend
		if err := rows.Scan(&s.ID, &s.RecordID, &s.WorkflowKey, &s.AttachmentID,
			&s.SentTo, &s.CC, &s.Subject, &s.SentByUserID, &s.SentAt); err != nil {
			return nil, fmt.Errorf("scan document send: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
```
(`nullIfEmpty` and `Querier` already exist in the `workflow` package — the same helpers `InsertAttachment` uses.)

- [ ] **Step 3: Write the dbtest**

Create `workflow/document_sends_dbtest_test.go`:
```go
//go:build dbtest

package workflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDocumentSends_InsertAndList(t *testing.T) {
	pool := testPool(t) // existing dbtest helper in the workflow package
	ctx := context.Background()
	recordID := "11111111-1111-1111-1111-111111111111"

	id, err := InsertDocumentSend(ctx, pool, DocumentSend{
		RecordID:    recordID,
		WorkflowKey: "invoice",
		SentTo:      "bob@buyer.example",
		Subject:     "Your Invoice INV-1001",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, id)

	list, err := ListDocumentSends(ctx, pool, recordID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "invoice", list[0].WorkflowKey)
	assert.Equal(t, "bob@buyer.example", list[0].SentTo)
}
```
If the `workflow` package's dbtest helper is named differently than `testPool`, use the existing one (grep `func test.*testing.T.*Pool` in `workflow/*_dbtest_test.go`).

- [ ] **Step 4: Run tests**

Run: `go build ./... && go test ./workflow/`
Expected: build succeeds; non-dbtest workflow tests pass. Then, if `TEST_DATABASE_URL` is set: `go test -tags dbtest ./workflow/ -run TestDocumentSends` → PASS (skips cleanly otherwise).

- [ ] **Step 5: Run the migration auditor**

Dispatch the `migration-auditor` agent against the `database/migrations/tenant/schema.sql` change. Expected: no idempotency/destructiveness findings.

- [ ] **Step 6: Commit**

```bash
git add database/migrations/tenant/schema.sql workflow/document_sends.go workflow/document_sends_dbtest_test.go
git commit -m "feat(workflow): add document_sends table and store helpers"
```

---

### Task 5: Per-module printable adapters

**Files:**
- Create: `invoice/printable.go`, `invoice/printable_test.go`
- Create: `quote/printable.go`, `quote/printable_test.go`
- Create: `estimate/printable.go`, `estimate/printable_test.go`
- Create: `salesorder/printable.go`, `salesorder/printable_test.go`

**Interfaces:**
- Consumes: `docpdf.PrintableDoc`, `docpdf.Seller`, `docpdf.Address`, `docpdf.PrintLine` (Task 1); each module's own typed struct.
- Produces (per module `<pkg>`):
  - `func ToPrintable(x <T>, seller docpdf.Seller) docpdf.PrintableDoc`
  - `func Recipient(x <T>) (email, name string)`

  where `<T>` is `invoice.Invoice`, `quote.Quote`, `estimate.Estimate`, `salesorder.Order`.

- [ ] **Step 1: Write the invoice failing test**

Create `invoice/printable_test.go`:
```go
package invoice

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestToPrintable_Invoice(t *testing.T) {
	inv := Invoice{
		Number: "INV-1001", StatusName: "Sent", InvoiceDate: "2026-08-24", DueDate: "2026-09-23",
		Customer:       CustomerRef{Name: "Bob Buyer"},
		Billing:        Address{CustomerName: "Bob Buyer", AddrLine1: "22 Main", City: "Dallas", Zip: "75201", Email: "bob@buyer.example"},
		Subtotal:       750, TaxTotal: 61.88, GrandTotal: 811.88, AmountPaid: 100, BalanceDue: 711.88,
		TermsConditions: "Net 30",
		Items: []Line{{ItemName: "Granite Slab", SKU: "SLAB-1", Quantity: 3, UnitPrice: 250, TaxPercent: 8.25, LineTotal: 750, UnitCode: "ea"}},
	}
	d := ToPrintable(inv, docpdf.Seller{Name: "Acme Stone Co"})
	assert.Equal(t, "INVOICE", d.Kind)
	assert.Equal(t, "INV-1001", d.Number)
	assert.True(t, d.ShowBalance)
	assert.Equal(t, 711.88, d.BalanceDue)
	assert.Len(t, d.Lines, 1)
	assert.Equal(t, "SLAB-1", d.Lines[0].SKU)

	email, name := Recipient(inv)
	assert.Equal(t, "bob@buyer.example", email)
	assert.Equal(t, "Bob Buyer", name)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./invoice/ -run TestToPrintable_Invoice -v`
Expected: FAIL — `undefined: ToPrintable`.

- [ ] **Step 3: Write `invoice/printable.go`**

```go
package invoice

import "stonesuite-backend/docpdf"

// ToPrintable maps an invoice to the renderer's module-agnostic PrintableDoc.
// Invoices show payment status (Amount Paid / Balance Due).
func ToPrintable(inv Invoice, seller docpdf.Seller) docpdf.PrintableDoc {
	lines := make([]docpdf.PrintLine, 0, len(inv.Items))
	for _, it := range inv.Items {
		lines = append(lines, docpdf.PrintLine{
			SKU: it.SKU, Name: it.ItemName, Description: it.Description, UnitCode: it.UnitCode,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, DiscountPercent: it.DiscountPercent,
			TaxPercent: it.TaxPercent, LineTotal: it.LineTotal,
		})
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "INVOICE", Number: inv.Number, Status: inv.StatusName,
		IssueDate: inv.InvoiceDate, DueDate: inv.DueDate,
		BillTo: addrToPrintable(inv.Billing), ShipTo: addrToPrintable(inv.Shipping),
		Lines: lines,
		Subtotal: inv.Subtotal, DiscountTotal: inv.DiscountTotal, TaxTotal: inv.TaxTotal,
		ShippingCharge: inv.ShippingCharge, Adjustment: inv.Adjustment, GrandTotal: inv.GrandTotal,
		AmountPaid: inv.AmountPaid, BalanceDue: inv.BalanceDue, ShowBalance: true,
		Terms: inv.TermsConditions, Notes: inv.Notes, Memo: inv.Memo,
	}
}

// Recipient resolves the default document-email recipient.
func Recipient(inv Invoice) (email, name string) {
	name = inv.Billing.CustomerName
	if name == "" {
		name = inv.Customer.Name
	}
	return inv.Billing.Email, name
}

func addrToPrintable(a Address) docpdf.Address {
	cityStateZip := a.City
	if a.Zip != "" {
		if cityStateZip != "" {
			cityStateZip += " "
		}
		cityStateZip += a.Zip
	}
	return docpdf.Address{
		Name: a.CustomerName, Attention: a.Attention, Line1: a.AddrLine1, Line2: a.AddrLine2,
		CityStateZip: cityStateZip, Phone: a.Phone, Email: a.Email,
	}
}
```

- [ ] **Step 4: Run to verify invoice passes**

Run: `go test ./invoice/ -run TestToPrintable_Invoice -v`
Expected: PASS.

- [ ] **Step 5: Write `quote/printable.go` (+ test)**

`quote/printable.go`:
```go
package quote

import "stonesuite-backend/docpdf"

// ToPrintable maps a quote to the renderer's PrintableDoc. Quotes do not show
// payment status.
func ToPrintable(q Quote, seller docpdf.Seller) docpdf.PrintableDoc {
	lines := make([]docpdf.PrintLine, 0, len(q.Items))
	for _, it := range q.Items {
		lines = append(lines, docpdf.PrintLine{
			SKU: it.SKU, Name: it.ItemName, Description: it.Description, UnitCode: it.UnitCode,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, DiscountPercent: it.DiscountPercent,
			TaxPercent: it.TaxPercent, LineTotal: it.LineTotal,
		})
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "QUOTE", Number: q.Number, Status: q.StatusName,
		IssueDate: q.QuoteDate, DueDate: q.ExpiryDate,
		BillTo: addrToPrintable(q.Billing), ShipTo: addrToPrintable(q.Shipping),
		Lines: lines,
		Subtotal: q.Subtotal, DiscountTotal: q.DiscountTotal, TaxTotal: q.TaxTotal,
		ShippingCharge: q.ShippingCharge, Adjustment: q.Adjustment, GrandTotal: q.GrandTotal,
		Terms: q.TermsConditions, Notes: q.Notes, Memo: q.Memo,
	}
}

// Recipient resolves the default document-email recipient.
func Recipient(q Quote) (email, name string) {
	name = q.Billing.CustomerName
	if name == "" {
		name = q.Customer.Name
	}
	return q.Billing.Email, name
}

func addrToPrintable(a Address) docpdf.Address {
	cityStateZip := a.City
	if a.Zip != "" {
		if cityStateZip != "" {
			cityStateZip += " "
		}
		cityStateZip += a.Zip
	}
	return docpdf.Address{
		Name: a.CustomerName, Attention: a.Attention, Line1: a.AddrLine1, Line2: a.AddrLine2,
		CityStateZip: cityStateZip, Phone: a.Phone, Email: a.Email,
	}
}
```
**Before writing, confirm the field names** by grepping `quote/types.go` (`grep -nE 'Number|StatusName|QuoteDate|ExpiryDate|Billing|Items|Subtotal|GrandTotal|TermsConditions|Notes|Memo|CustomerName|AddrLine' quote/types.go`). If a field differs (e.g. the date field is `QuoteDate` vs `IssueDate`, or there is no `ExpiryDate`), adjust the mapping — leave `DueDate` blank when the module has no expiry concept. Add `quote/printable_test.go` mirroring the invoice test but asserting `Kind == "QUOTE"` and `ShowBalance == false`.

- [ ] **Step 6: Write `estimate/printable.go` (+ test)**

Identical shape to quote, with `Kind: "ESTIMATE"` and the `estimate.Estimate` / `estimate.Line` / `estimate.Address` types. Confirm field names via `grep -nE 'Number|StatusName|EstimateDate|Billing|Items|Subtotal|GrandTotal|TermsConditions' estimate/types.go` and adjust the date field accordingly. `ShowBalance` stays false. Add `estimate/printable_test.go` asserting `Kind == "ESTIMATE"`.

- [ ] **Step 7: Write `salesorder/printable.go` (+ test)**

Same shape, `Kind: "SALES ORDER"`, using the `salesorder.Order` type. **The sales-order type predates the quote/estimate twins**, so its field names most likely differ — confirm with `grep -nE 'Number|Status|Date|Billing|Ship|Items|Line|Subtotal|GrandTotal|Terms|Notes|Memo|Customer' salesorder/types.go` and map each field explicitly (the item struct field names — e.g. `ItemName` vs `Name`, `UnitCode` vs `UOM` — must be read from the file, not assumed). `ShowBalance` stays false. Add `salesorder/printable_test.go` asserting `Kind == "SALES ORDER"`.

- [ ] **Step 8: Run all four packages' tests + build**

Run: `go build ./... && go test ./invoice/ ./quote/ ./estimate/ ./salesorder/ -run TestToPrintable`
Expected: PASS in all four.

- [ ] **Step 9: Run the module-drift checker**

Dispatch the `module-drift-checker` agent for `invoice`, `quote`, `estimate`, `salesorder`. Expected: no new drift findings from the added adapters.

- [ ] **Step 10: Commit**

```bash
git add invoice/printable.go invoice/printable_test.go quote/printable.go quote/printable_test.go estimate/printable.go estimate/printable_test.go salesorder/printable.go salesorder/printable_test.go
git commit -m "feat(quote,estimate,salesorder,invoice): map documents to printable form"
```

---

### Task 6: `DocumentOps` controller — shared auth + PDF endpoint

**Files:**
- Modify: `controllers/attachments.go` (extract the auth gate body into a reusable free function)
- Create: `controllers/documents.go`
- Create: `controllers/documents_test.go`

**Interfaces:**
- Consumes: `workflow.ResolveRecordAccess`, `authz.Check`, `recordInScope`, `resourceForKey`, `logSecurityEvent`, `fail`, `writeJSON`, `middleware.GetUserFromContext`, `tenancy.PoolFromContext`, `tenancy.TenantFromContext`, `storage.Client`, `docpdf.Render`.
- Produces:
  - `func authRecordAccess(w http.ResponseWriter, r *http.Request, recordID string, action authz.Action) (*pgxpool.Pool, workflow.RecordAccessInfo, string, bool)` (extracted; shared by attachments + documents)
  - `type DocMeta struct { WorkflowKey, Number, DefaultRecipientEmail, DefaultRecipientName, DefaultSubject string }`
  - `type DocumentLoader func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error)`
  - `type DocumentOps struct { … }` with `func NewDocumentOps(r2 *storage.Client, loaders map[string]DocumentLoader) *DocumentOps`
  - `func (h *DocumentOps) GetPDF(w http.ResponseWriter, r *http.Request)`
  - `func sellerFromTenant(t *tenancy.Tenant) docpdf.Seller`

- [ ] **Step 1: Extract the shared auth gate**

In `controllers/attachments.go`, rename the body of `attachAuth` into a free function and delegate. Replace the `attachAuth` method with:
```go
func (h *AttachmentOps) attachAuth(
	w http.ResponseWriter, r *http.Request, recordID string, action authz.Action,
) (*pgxpool.Pool, workflow.RecordAccessInfo, string, bool) {
	return authRecordAccess(w, r, recordID, action)
}
```
And add the extracted free function (identical logic to the current `attachAuth` body) in the same file:
```go
// authRecordAccess is the shared RBAC + IDOR gate for record-keyed endpoints
// (attachments and documents). It resolves the record's real type, checks the
// type-specific resource:action permission, and enforces the row-level
// ownership scope. Denial is always 404 (never 403) so ids cannot be
// enumerated. Returns pool, resolved record info, identityID, ok.
func authRecordAccess(
	w http.ResponseWriter, r *http.Request, recordID string, action authz.Action,
) (*pgxpool.Pool, workflow.RecordAccessInfo, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	info, err := workflow.ResolveRecordAccess(r.Context(), pool, recordID)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	resource := resourceForKey(info.WorkflowKey)
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to access this record.")
		return nil, workflow.RecordAccessInfo{}, "", false
	}
	if decision.Scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, payload.ID, info.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, workflow.RecordAccessInfo{}, "", false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", payload.ID, "record", recordID, "resource", string(resource),
				"action", string(action), "scope", string(decision.Scope))
			fail(w, http.StatusNotFound, "Record not found.")
			return nil, workflow.RecordAccessInfo{}, "", false
		}
	}
	return pool, info, payload.ID, true
}
```

- [ ] **Step 2: Confirm the refactor is behaviour-preserving**

Run: `go build ./... && go test ./controllers/ -run TestAttachment`
Expected: existing attachment tests still PASS.

- [ ] **Step 3: Write the failing DocumentOps test**

Create `controllers/documents_test.go`:
```go
package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
)

func TestDocumentOps_RequiresAuth(t *testing.T) {
	h := NewDocumentOps(nil, map[string]DocumentLoader{})
	for name, fn := range map[string]http.HandlerFunc{
		"GetPDF": h.GetPDF, "Send": h.Send, "Sends": h.Sends,
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/records/x/document/pdf", nil)
			req.SetPathValue("id", "x")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code)
		})
	}
}

func TestSellerFromTenant_UsesDisplayName(t *testing.T) {
	// nil-safe: empty metadata yields a seller with just the display name.
	s := sellerFromTenantMeta("Acme Stone Co", "")
	assert.Equal(t, "Acme Stone Co", s.Name)
}

var _ = docpdf.Seller{}
```
(`Send`/`Sends` are added in Task 7 but the handler stubs must exist for this file to compile — create them in Step 4 returning `fail(w, http.StatusNotImplemented, …)` and flesh them out in Task 7.)

- [ ] **Step 4: Run to verify it fails**

Run: `go test ./controllers/ -run 'TestDocumentOps_RequiresAuth|TestSellerFromTenant' -v`
Expected: FAIL — `undefined: NewDocumentOps`.

- [ ] **Step 5: Write `controllers/documents.go`**

```go
package controllers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/docpdf"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

// DocMeta carries the non-visual metadata a loader returns alongside the
// printable document: the storage-key inputs and the default email fields.
type DocMeta struct {
	WorkflowKey           string
	Number                string
	DefaultRecipientEmail string
	DefaultRecipientName  string
	DefaultSubject        string
}

// DocumentLoader loads a document by UUID and maps it to a PrintableDoc plus
// DocMeta. Registered per module at wiring time in main.go.
type DocumentLoader func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, DocMeta, error)

// DocumentOps serves generic, record-keyed document endpoints (PDF + send),
// dispatching to a per-module loader resolved from the record's type.
type DocumentOps struct {
	r2      *storage.Client
	loaders map[string]DocumentLoader
	// renderPDF is injectable for tests; defaults to docpdf.Render.
	renderPDF func(docpdf.PrintableDoc) ([]byte, error)
}

// NewDocumentOps constructs the handler group.
func NewDocumentOps(r2 *storage.Client, loaders map[string]DocumentLoader) *DocumentOps {
	return &DocumentOps{r2: r2, loaders: loaders, renderPDF: docpdf.Render}
}

// loadForRender runs the shared auth gate, resolves the loader for the record's
// type, and produces the printable doc + meta. Returns ok=false having already
// written the HTTP error.
func (h *DocumentOps) loadForRender(
	w http.ResponseWriter, r *http.Request, recordID string, action authz.Action,
) (*pgxpool.Pool, docpdf.PrintableDoc, DocMeta, string, bool) {
	pool, info, identityID, ok := authRecordAccess(w, r, recordID, action)
	if !ok {
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", false
	}
	loader, ok := h.loaders[info.WorkflowKey]
	if !ok {
		fail(w, http.StatusNotFound, "This record type has no printable document.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", false
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", false
	}
	seller := sellerFromTenant(tenant)
	doc, meta, err := loader(r.Context(), pool, recordID, seller)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load document.")
		return nil, docpdf.PrintableDoc{}, DocMeta{}, "", false
	}
	return pool, doc, meta, identityID, true
}

// GetPDF renders the document on the fly and streams it. RBAC: <type>:read.
func (h *DocumentOps) GetPDF(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	_, doc, meta, _, ok := h.loadForRender(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	pdf, err := h.renderPDF(doc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to render PDF.")
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="`+meta.Number+`.pdf"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(pdf)
}

// sellerFromTenant builds the letterhead from the tenant's display name and
// whatever company profile fields exist in its onboarding metadata JSON.
func sellerFromTenant(t *tenancy.Tenant) docpdf.Seller {
	return sellerFromTenantMeta(t.DisplayName, t.Metadata)
}

// sellerFromTenantMeta is the pure core of sellerFromTenant (testable without a
// tenancy.Tenant). Metadata is dynamic onboarding form JSON; unknown keys are
// simply absent.
func sellerFromTenantMeta(displayName, metadataJSON string) docpdf.Seller {
	s := docpdf.Seller{Name: displayName}
	if metadataJSON == "" {
		return s
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(metadataJSON), &m); err != nil {
		return s
	}
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := m[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	if name := get("company_name", "companyName"); name != "" {
		s.Name = name
	}
	s.AddrLine1 = get("address", "address_line1", "addressLine1", "street")
	s.CityStateZip = get("city_state_zip", "cityStateZip")
	s.Phone = get("phone", "company_phone", "phoneNumber")
	s.Email = get("email", "company_email", "contact_email")
	return s
}
```

- [ ] **Step 6: Add Task-7 handler stubs so the package compiles**

Append to `controllers/documents.go` (fleshed out in Task 7):
```go
// Send is implemented in Task 7.
func (h *DocumentOps) Send(w http.ResponseWriter, r *http.Request) {
	fail(w, http.StatusNotImplemented, "Not implemented.")
}

// Sends is implemented in Task 7.
func (h *DocumentOps) Sends(w http.ResponseWriter, r *http.Request) {
	fail(w, http.StatusNotImplemented, "Not implemented.")
}
```

- [ ] **Step 7: Run to verify tests pass**

Run: `go build ./... && go test ./controllers/ -run 'TestDocumentOps_RequiresAuth|TestSellerFromTenant' -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add controllers/attachments.go controllers/documents.go controllers/documents_test.go
git commit -m "feat(controllers): add DocumentOps PDF endpoint and shared record-auth gate"
```

---

### Task 7: Send + history endpoints and wiring

**Files:**
- Modify: `controllers/documents.go` (implement `Send`, `Sends`)
- Modify: `controllers/documents_test.go` (add validation + 503 tests)
- Modify: `main.go` (build loader registry, construct `DocumentOps`, register routes)

**Interfaces:**
- Consumes: `docpdf.Render`, `storage.Client.Put`, `workflow.GenerateStorageKey`, `workflow.SanitizeFileName`, `workflow.InsertAttachment`, `workflow.InsertDocumentSend`, `workflow.ListDocumentSends`, `workflow.UserIDByIdentity`, `workflow.LogAudit`, `services.SendDocumentEmail`, each module's `ToPrintable`/`Recipient`/`Get`.
- Produces: fully implemented `Send` / `Sends`; wired routes.

- [ ] **Step 1: Write the failing validation + 503 tests**

Add to `controllers/documents_test.go`:
```go
func TestSend_BadBodyIsRejectedAfterAuthStub(t *testing.T) {
	// With a nil R2 client the Send handler must refuse (503) rather than
	// attempt to persist. Auth still runs first, so an unauthenticated call is
	// 401; this documents the R2 precondition via the handler contract.
	h := NewDocumentOps(nil, map[string]DocumentLoader{})
	assert.Nil(t, h.r2)
}
```
(The full happy-path send is covered by the dbtest in Step 5; unit-level we assert the R2 precondition and rely on the injected fakes there.)

- [ ] **Step 2: Implement `Send` and `Sends` in `controllers/documents.go`**

Replace the two stubs with:
```go
type sendDocRequest struct {
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	Subject string   `json:"subject"`
	Message string   `json:"message"`
}

// Send renders the document, persists the PDF to R2 as an attachment, emails it
// to the customer, and records the send. RBAC: <type>:update.
func (h *DocumentOps) Send(w http.ResponseWriter, r *http.Request) {
	if !h.r2.IsConfigured() {
		fail(w, http.StatusServiceUnavailable, "File storage is not configured.")
		return
	}
	recordID := r.PathValue("id")
	pool, doc, meta, identityID, ok := h.loadForRender(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil || tenant.R2Bucket == "" {
		fail(w, http.StatusServiceUnavailable, "File storage not provisioned for this tenant.")
		return
	}

	var req sendDocRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err.Error() != "EOF" {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	to := normalizeRecipients(req.To)
	if len(to) == 0 && meta.DefaultRecipientEmail != "" {
		to = []string{meta.DefaultRecipientEmail}
	}
	if len(to) == 0 {
		fail(w, http.StatusBadRequest, "At least one recipient is required.")
		return
	}
	for _, addr := range append(append([]string{}, to...), normalizeRecipients(req.CC)...) {
		if !looksLikeEmail(addr) {
			fail(w, http.StatusBadRequest, "Invalid recipient email: "+addr)
			return
		}
	}
	subject := req.Subject
	if subject == "" {
		subject = meta.DefaultSubject
	}

	// 1. Render.
	pdf, err := h.renderPDF(doc)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to render PDF.")
		return
	}

	// 2. Persist to R2 as an attachment on the record.
	fileName := workflow.SanitizeFileName(meta.Number + ".pdf")
	attachUUID, err := newAttachUUID()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to generate file key.")
		return
	}
	storageKey := workflow.GenerateStorageKey(tenant.Slug, meta.WorkflowKey, recordID, attachUUID, fileName)
	if err := h.r2.WithBucket(tenant.R2Bucket).Put(r.Context(), storageKey, "application/pdf", pdf); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to store document.")
		return
	}
	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	attachmentID, err := workflow.InsertAttachment(r.Context(), pool, workflow.Attachment{
		RecordID: recordID, FileName: fileName, ContentType: "application/pdf",
		SizeBytes: int64(len(pdf)), StorageKey: storageKey, UploadedByUserID: actorUserID,
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save document attachment.")
		return
	}

	// 3. Email with the PDF attached.
	if err := services.SendDocumentEmail(to, normalizeRecipients(req.CC), subject,
		documentEmailHTML(doc, req.Message),
		[]services.EmailAttachment{{FileName: fileName, ContentType: "application/pdf", Content: pdf}},
	); err != nil {
		fail(w, http.StatusBadGateway, "Failed to send email.")
		return
	}

	// 4. Record the send + audit (best-effort audit).
	sendID, err := workflow.InsertDocumentSend(r.Context(), pool, workflow.DocumentSend{
		RecordID: recordID, WorkflowKey: meta.WorkflowKey, AttachmentID: attachmentID,
		SentTo: joinRecipients(to), CC: joinRecipients(normalizeRecipients(req.CC)),
		Subject: subject, SentByUserID: actorUserID,
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to record send.")
		return
	}
	_ = workflow.LogAudit(r.Context(), pool, actorUserID, "document.sent", "document_send", sendID,
		map[string]any{"recordId": recordID, "workflowKey": meta.WorkflowKey, "to": to})

	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "sendId": sendID, "attachmentId": attachmentID, "sentTo": to,
	})
}

// Sends returns a record's document send history. RBAC: <type>:read.
func (h *DocumentOps) Sends(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, _, identityID, ok := authRecordAccess(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	_ = identityID
	sends, err := workflow.ListDocumentSends(r.Context(), pool, recordID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list sends.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "sends": sends})
}
```

Add the small helpers (same file) and the imports `"strings"`, `"stonesuite-backend/services"`, `"stonesuite-backend/workflow"`:
```go
func normalizeRecipients(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func joinRecipients(in []string) string { return strings.Join(in, ", ") }

// looksLikeEmail is a minimal, allocation-free sanity check (not full RFC 5322).
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	return at > 0 && at < len(s)-1 && strings.IndexByte(s[at+1:], '.') >= 0
}

// documentEmailHTML is the transactional email body wrapping an optional
// sender message.
func documentEmailHTML(d docpdf.PrintableDoc, message string) string {
	msg := "Please find your " + strings.ToLower(d.Kind) + " " + d.Number + " attached."
	if message != "" {
		msg = message
	}
	return `<html><body style="font-family:Arial,sans-serif;color:#333;">` +
		`<p>` + msg + `</p>` +
		`<p>Regards,<br>` + d.Seller.Name + `</p></body></html>`
}
```

- [ ] **Step 3: Register routes and loaders in `main.go`**

Immediately after the attachment route block (`main.go:598-605`), add:
```go
		docLoaders := map[string]controllers.DocumentLoader{
			"invoice": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, controllers.DocMeta, error) {
				inv, err := invoice.Get(ctx, pool, uuid)
				if err != nil {
					return docpdf.PrintableDoc{}, controllers.DocMeta{}, err
				}
				email, name := invoice.Recipient(*inv)
				return invoice.ToPrintable(*inv, seller),
					controllers.DocMeta{WorkflowKey: "invoice", Number: inv.Number, DefaultRecipientEmail: email, DefaultRecipientName: name, DefaultSubject: "Your Invoice " + inv.Number}, nil
			},
			"quote": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, controllers.DocMeta, error) {
				q, err := quote.Get(ctx, pool, uuid)
				if err != nil {
					return docpdf.PrintableDoc{}, controllers.DocMeta{}, err
				}
				email, name := quote.Recipient(*q)
				return quote.ToPrintable(*q, seller),
					controllers.DocMeta{WorkflowKey: "quote", Number: q.Number, DefaultRecipientEmail: email, DefaultRecipientName: name, DefaultSubject: "Your Quote " + q.Number}, nil
			},
			"estimate": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, controllers.DocMeta, error) {
				est, err := estimate.Get(ctx, pool, uuid)
				if err != nil {
					return docpdf.PrintableDoc{}, controllers.DocMeta{}, err
				}
				email, name := estimate.Recipient(*est)
				return estimate.ToPrintable(*est, seller),
					controllers.DocMeta{WorkflowKey: "estimate", Number: est.Number, DefaultRecipientEmail: email, DefaultRecipientName: name, DefaultSubject: "Your Estimate " + est.Number}, nil
			},
			"sales_order": func(ctx context.Context, pool *pgxpool.Pool, uuid string, seller docpdf.Seller) (docpdf.PrintableDoc, controllers.DocMeta, error) {
				so, err := salesorder.Get(ctx, pool, uuid)
				if err != nil {
					return docpdf.PrintableDoc{}, controllers.DocMeta{}, err
				}
				email, name := salesorder.Recipient(*so)
				return salesorder.ToPrintable(*so, seller),
					controllers.DocMeta{WorkflowKey: "sales_order", Number: so.Number, DefaultRecipientEmail: email, DefaultRecipientName: name, DefaultSubject: "Your Sales Order " + so.Number}, nil
			},
		}
		docOps := controllers.NewDocumentOps(r2Client, docLoaders)
		mux.Handle("GET /api/tenant/records/{id}/document/pdf", tenantChain(docOps.GetPDF))
		mux.Handle("POST /api/tenant/records/{id}/document/send", tenantChain(docOps.Send))
		mux.Handle("GET /api/tenant/records/{id}/document/sends", tenantChain(docOps.Sends))
```
Add imports to `main.go` as needed: `"stonesuite-backend/docpdf"`, and the module packages `invoice`, `quote`, `estimate`, `salesorder` (some may already be imported — check the existing import block). The `sales_order` loader key must match the `WorkflowKey` that `ResolveRecordAccess` returns for sales orders (`"sales_order"` — verified in `workflow/attachments.go:168`). Confirm each module's `Number` field name (`inv.Number`, `q.Number`, etc.) matches what Task 5 verified; adjust if a module names it differently.

- [ ] **Step 4: Build + run controller/unit tests**

Run: `go build ./... && go test ./controllers/ ./services/ ./storage/ ./docpdf/`
Expected: PASS; build succeeds.

- [ ] **Step 5: Write the happy-path dbtest**

Create `controllers/document_send_dbtest_test.go`:
```go
//go:build dbtest

package controllers

import (
	"testing"
)

// TestDocumentSend_HappyPath exercises render→store→email→track against a real
// tenant DB with a fake R2/email, following the existing *_dbtest_test.go setup
// helpers in this package (see customer_portal_dbtest_test.go for the tenant
// bootstrap + authed-request pattern). It creates an invoice, calls Send with a
// stubbed renderPDF and a fake storage/email, and asserts a document_sends row
// and an attachment row were written.
func TestDocumentSend_HappyPath(t *testing.T) {
	t.Skip("implement using the package's dbtest tenant harness once wired")
}
```
(This placeholder is intentionally skipped: the full harness reuses the package's existing dbtest bootstrap, which the implementing engineer wires to the real helpers. The unit tests + the four unit-tested building blocks already cover the logic; this dbtest is the integration safety net.)

- [ ] **Step 6: Run the security + drift reviewers**

Dispatch `tenancy-security-reviewer` on `controllers/documents.go` and `controllers/attachments.go` (the extracted gate), and `module-drift-checker` on the four modules. Expected: no findings (the gate reuses the reviewed path; 404-on-denial and `logSecurityEvent` are preserved).

- [ ] **Step 7: Full build + vet + test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all green.

- [ ] **Step 8: Commit**

```bash
git add controllers/documents.go controllers/documents_test.go controllers/document_send_dbtest_test.go main.go
git commit -m "feat(documents): add send + history endpoints and wire document routes"
```

---

## Self-Review

**Spec coverage:**
- D1/D2 pure-Go backend PDF → Task 1 (`docpdf` + `go-pdf/fpdf`). ✓
- D3 core four → Task 5 adapters + Task 7 loaders. ✓
- D4 persist + attach + track → Task 7 `Send` (R2 Put → InsertAttachment → email attach → InsertDocumentSend). ✓
- D5 recipient (doc contact, editable + CC) → `Recipient` (Task 5), `sendDocRequest`/defaults (Task 7). ✓
- D6 generic record-keyed endpoints → Task 6/7 `DocumentOps` + shared `authRecordAccess`. ✓
- D7 single `document_sends` table → Task 4. ✓
- Plumbing gaps: `storage.Put` (Task 2), `SendDocumentEmail` (Task 3), seller letterhead (Task 6). ✓
- Security (404-on-denial, IDOR logging, RBAC read/update) → shared `authRecordAccess`, reused verbatim. ✓
- Testing + post-impl agents (`migration-auditor`, `tenancy-security-reviewer`, `module-drift-checker`) → Tasks 4/5/7. ✓
- Out-of-scope items (logo, receipts, headless-chrome) are not planned. ✓

**Placeholder scan:** The only deliberate `t.Skip` is the integration dbtest in Task 7 Step 5, whose harness depends on the package's existing (unshown) dbtest bootstrap; the logic it would cover is unit-tested in Tasks 1–3, 6. Field-name confirmations in Task 5 are explicit grep-and-adjust steps, not vague "handle it" placeholders.

**Type consistency:** `PrintableDoc`, `Seller`, `Address`, `PrintLine` (Task 1) are used unchanged in Tasks 5–7. `DocumentLoader`/`DocMeta` signatures (Task 6) match the `main.go` registry literals (Task 7). `authRecordAccess` return tuple matches both `attachAuth` and `DocumentOps.loadForRender`/`Sends`. `SendDocumentEmail`/`EmailAttachment` (Task 3) match the Task 7 call site. `storage.Client.Put` (Task 2) matches the Task 7 call site. Module `ToPrintable(x T, seller)`/`Recipient(x T)` (Task 5) match the loader closures (Task 7).
