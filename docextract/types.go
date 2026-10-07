package docextract

import "fmt"

// DocType is the kind of business document being created from an upload.
type DocType string

// Supported target document types.
const (
	DocTypeSalesOrder    DocType = "sales_order"
	DocTypePurchaseOrder DocType = "purchase_order"
	DocTypeVendorBill    DocType = "vendor_bill"
)

// FailureCode is a stable, non-retryable input failure identifier.
type FailureCode string

// Failure codes surfaced to the review UI.
const (
	FailCorrupt               FailureCode = "corrupt"
	FailScanned               FailureCode = "scanned"
	FailUnreadableText        FailureCode = "unreadable_text"
	FailPasswordProtected     FailureCode = "password_protected"
	FailUnsupportedEncryption FailureCode = "unsupported_encryption"
	FailPageCap               FailureCode = "page_cap"
	FailLineCap               FailureCode = "line_cap"
	FailTooLarge              FailureCode = "too_large"
	FailUnsupportedType       FailureCode = "unsupported_type"
	FailEmpty                 FailureCode = "empty"
)

// InputError is a non-retryable failure caused by the uploaded file itself.
type InputError struct {
	Code   FailureCode
	Detail string
}

// Error implements error.
func (e *InputError) Error() string {
	if e.Detail == "" {
		return string(e.Code)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Detail)
}

// newInputError builds an InputError.
func newInputError(code FailureCode, detail string) *InputError {
	return &InputError{Code: code, Detail: detail}
}

// Source says where a field value came from.
type Source string

// Field sources.
const (
	SourceDocument Source = "document"
	SourceLearned  Source = "learned"
	SourceCatalog  Source = "catalog"
	SourceDefault  Source = "default"
)

// Confidence is a reviewer-facing confidence word, never a raw score.
type Confidence string

// Confidence levels.
const (
	ConfHigh     Confidence = "high"
	ConfCheck    Confidence = "check"
	ConfNotFound Confidence = "not_found"
)

// MaxSnippetLen caps the source snippet carried on a Field.
const MaxSnippetLen = 120

// Field is one extracted value with provenance.
type Field struct {
	Value      string     `json:"value"`
	Source     Source     `json:"source"`
	Confidence Confidence `json:"confidence"`
	Snippet    string     `json:"snippet,omitempty"`
	Page       int        `json:"page,omitempty"`
	Row        int        `json:"row,omitempty"`
}

// Found reports whether the field carries a value.
func (f Field) Found() bool { return f.Value != "" }

// notFound is the zero-confidence field.
func notFound() Field { return Field{Source: SourceDocument, Confidence: ConfNotFound} }

// LineKind classifies a table row.
type LineKind string

// Line kinds.
const (
	KindProduct LineKind = "product"
	KindAddon   LineKind = "addon"
	KindCharge  LineKind = "charge"
	KindNote    LineKind = "note"
)

// Line flags raised by the table parser.
const (
	FlagQtyUnparsed    = "qty_unparsed"
	FlagQtyRange       = "qty_out_of_range"
	FlagPriceUnparsed  = "price_unparsed"
	FlagAmountUnparsed = "amount_unparsed"
	FlagAmountMissing  = "amount_missing"
	FlagAmountMismatch = "amount_mismatch"
	FlagNegativePrice  = "negative_price"
	FlagEUDecimal      = "eu_decimal"
	FlagPricePrecision = "price_precision"
)

// Line is one parsed document line.
type Line struct {
	Kind        LineKind `json:"kind"`
	ParentLine  int      `json:"parentLine,omitempty"` // 1-based index into Result.Lines; 0 = none
	SKU         Field    `json:"sku"`
	Description Field    `json:"description"`
	UoM         Field    `json:"uom"`
	Qty         Field    `json:"qty"`
	UnitPrice   Field    `json:"unitPrice"`
	Amount      Field    `json:"amount"`
	// Detail is extra description that is not part of the item's name
	// (an order form area's finish, edge and cutouts); never used to match.
	Detail string `json:"detail,omitempty"`

	QtyMilli       int64    `json:"qtyMilli"`
	UnitPriceCents Cents    `json:"unitPriceCents"`
	AmountCents    Cents    `json:"amountCents"`
	Flags          []string `json:"flags,omitempty"`
}

// Header holds the sales-order header fields.
type Header struct {
	PONumber     Field `json:"poNumber"`
	OrderDate    Field `json:"orderDate"`
	DeliveryDate Field `json:"deliveryDate"`
	CustomerName Field `json:"customerName"`
	BillTo       Field `json:"billTo"`
	ShipTo       Field `json:"shipTo"`
	PaymentTerms Field `json:"paymentTerms"`
	Subtotal     Field `json:"subtotal"`
	Tax          Field `json:"tax"`
	Shipping     Field `json:"shipping"`
	Discount     Field `json:"discount"`
	Total        Field `json:"total"`
	Currency     Field `json:"currency"`
	// Notes is free text for the order memo (an order form's special
	// instructions and job contact); empty for a priced PO.
	Notes Field `json:"notes"`
}

// Revision marks a document that amends or replaces an earlier one.
type Revision struct {
	Label            string `json:"label"`
	ReferencedNumber string `json:"referencedNumber,omitempty"`
}

// Word is one positioned word on a row. Form marks a value typed into a
// fillable-form field (as opposed to text printed on the page).
type Word struct {
	X    float64 `json:"x"`
	W    float64 `json:"w"`
	Text string  `json:"text"`
	Form bool    `json:"form,omitempty"`
}

// Row is one visual text row; rows are ordered top to bottom (Y descending).
type Row struct {
	Y     float64 `json:"y"`
	Words []Word  `json:"words"`
}

// PageRows is the text of one kept page.
type PageRows struct {
	Page int   `json:"page"`
	Rows []Row `json:"rows"`
}

// Result is everything the parser learned about a document.
type Result struct {
	DocType           DocType       `json:"docType"`
	Header            Header        `json:"header"`
	Lines             []Line        `json:"lines"`
	Checks            []CheckResult `json:"checks,omitempty"`
	Warnings          []string      `json:"warnings,omitempty"`
	Revision          *Revision     `json:"revision,omitempty"`
	QuoteRef          string        `json:"quoteRef,omitempty"`
	Signed            bool          `json:"signed"`
	Restricted        bool          `json:"restricted"`
	ClassifiedAs      string        `json:"classifiedAs,omitempty"`
	Pages             []PageRows    `json:"pages"`
	LayoutFingerprint string        `json:"layoutFingerprint,omitempty"`
	Unresolved        []string      `json:"unresolved,omitempty"`
	Injection         []string      `json:"injection,omitempty"`

	residualSpans map[int][2]int // table rows excluded from LLM residual text
}

// Limits bounds the work done on one document.
type Limits struct {
	MaxBytes int64
	MaxPages int
	MaxLines int
	MaxQty   int64 // whole units
}

// Default limits (mirrors the plan's G1 defaults).
const (
	DefaultMaxBytes = 10 << 20
	DefaultMaxPages = 20
	DefaultMaxLines = 200
	DefaultMaxQty   = 1_000_000
)

// DefaultLimits returns the G1 default limits.
func DefaultLimits() Limits {
	return Limits{MaxBytes: DefaultMaxBytes, MaxPages: DefaultMaxPages, MaxLines: DefaultMaxLines, MaxQty: DefaultMaxQty}
}

// withDefaults fills zero fields.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxBytes <= 0 {
		l.MaxBytes = d.MaxBytes
	}
	if l.MaxPages <= 0 {
		l.MaxPages = d.MaxPages
	}
	if l.MaxLines <= 0 {
		l.MaxLines = d.MaxLines
	}
	if l.MaxQty <= 0 {
		l.MaxQty = d.MaxQty
	}
	return l
}

// Text joins a row's words with single spaces.
func (r Row) Text() string {
	n := 0
	for _, w := range r.Words {
		n += len(w.Text) + 1
	}
	b := make([]byte, 0, n)
	for i, w := range r.Words {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, w.Text...)
	}
	return string(b)
}
