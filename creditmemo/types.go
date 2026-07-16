package creditmemo

import "time"

// CustomerRef is the flattened {id, name} for "who was credited" navigation.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InvoiceRef is the flattened {id, number} of the invoice a memo arose from.
// Lineage only — it carries no money semantics (spec AD-2). The
// credit_memo_application ledger is the only thing that moves balance.
type InvoiceRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

// Address is a credit memo's frozen billing snapshot.
type Address struct {
	CustomerName string `json:"customerName"`
	Attention    string `json:"attention"`
	Line1        string `json:"line1"`
	Line2        string `json:"line2"`
	SuiteNumber  string `json:"suiteNumber"`
	City         string `json:"city"`
	StateID      *int   `json:"stateId,omitempty"`
	Zip          string `json:"zip"`
	CountryID    *int   `json:"countryId,omitempty"`
	Phone        string `json:"phone"`
	Fax          string `json:"fax"`
	Email        string `json:"email"`
}

// Line is one live credit_memo_item row.
type Line struct {
	ID              string  `json:"id"`
	LineNumber      int     `json:"lineNumber"`
	InventoryItemID *int    `json:"inventoryItemId,omitempty"`
	InvoiceItemID   *int    `json:"invoiceItemId,omitempty"`
	ItemName        string  `json:"itemName"`
	SKU             string  `json:"sku"`
	Description     string  `json:"description"`
	UnitID          *int    `json:"unitId,omitempty"`
	UnitCode        string  `json:"unitCode"`
	Quantity        float64 `json:"quantity"`
	UnitPrice       float64 `json:"unitPrice"`
	DiscountPercent float64 `json:"discountPercent"`
	TaxRateID       *int    `json:"taxRateId,omitempty"`
	TaxPercent      float64 `json:"taxPercent"`
	LineSubtotal    float64 `json:"lineSubtotal"`
	LineDiscount    float64 `json:"lineDiscount"`
	LineTax         float64 `json:"lineTax"`
	LineTotal       float64 `json:"lineTotal"`
}

// Application is one live credit_memo_application row, joined with its
// invoice's display fields.
type Application struct {
	ID            string    `json:"id"`
	InvoiceID     string    `json:"invoiceId"`
	InvoiceNumber string    `json:"invoiceNumber"`
	Amount        float64   `json:"amount"`
	CreatedAt     time.Time `json:"createdAt"`
}

// CreditMemo is the credit memo header + its live lines and applications.
type CreditMemo struct {
	ID     string `json:"id"`
	Number string `json:"creditMemoNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	Customer     CustomerRef `json:"customer"`
	Invoice      *InvoiceRef `json:"invoice,omitempty"`
	SalesOrderID *string     `json:"salesOrderId,omitempty"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`
	SalesRepID      *int   `json:"salesRepId,omitempty"`

	ReferenceNumber string    `json:"referenceNumber"`
	CreditMemoDate  time.Time `json:"creditMemoDate"`
	Reason          string    `json:"reason"`
	SalesTaxPercent float64   `json:"salesTaxPercent"`
	Memo            string    `json:"memo"`
	Notes           string    `json:"notes"`
	InternalNotes   string    `json:"internalNotes"`

	PriceLevelID *int    `json:"priceLevelId,omitempty"`
	CurrencyID   *int    `json:"currencyId,omitempty"`
	ExchangeRate float64 `json:"exchangeRate"`

	Subtotal      float64 `json:"subtotal"`
	DiscountTotal float64 `json:"discountTotal"`
	TaxTotal      float64 `json:"taxTotal"`
	Adjustment    float64 `json:"adjustment"`
	GrandTotal    float64 `json:"grandTotal"`

	AppliedTotal    float64 `json:"appliedTotal"`
	UnappliedAmount float64 `json:"unappliedAmount"`

	BillingAddress Address `json:"billingAddress"`

	CustomFields map[string]any `json:"customFields"`
	Lines        []Line         `json:"lines"`
	Applications []Application  `json:"applications"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// CreditMemoLineInput is one line of a create/update request. Mirrors
// invoice.InvoiceLineInput: catalog items are addressed by uuid (the house
// convention for every external identifier), and a line with no
// inventoryItemUuid is a free-text line carrying its own description.
type CreditMemoLineInput struct {
	LineNumber        int     `json:"lineNumber"`
	InventoryItemUUID string  `json:"inventoryItemUuid"`
	Description       string  `json:"description"`
	Quantity          float64 `json:"quantity"`
	UnitPrice         float64 `json:"unitPrice"`
	DiscountPercent   float64 `json:"discountPercent"`
	TaxRateID         *int    `json:"taxRateId,omitempty"`
}

// ApplicationInput is one entry of a create/apply request.
type ApplicationInput struct {
	InvoiceUUID string  `json:"invoiceUuid"`
	Amount      float64 `json:"amount"`
}

// CreateCreditMemoInput is the request payload for POST /api/tenant/credit-memos.
type CreateCreditMemoInput struct {
	CustomerUUID    string                `json:"customerUuid"`
	InvoiceUUID     string                `json:"invoiceUuid"`
	SalesOrderUUID  string                `json:"salesOrderUuid"`
	ReferenceNumber string                `json:"referenceNumber"`
	CreditMemoDate  *time.Time            `json:"creditMemoDate,omitempty"`
	Reason          string                `json:"reason"`
	SalesTaxPercent float64               `json:"salesTaxPercent"`
	Adjustment      float64               `json:"adjustment"`
	PriceLevelID    *int                  `json:"priceLevelId,omitempty"`
	CurrencyID      *int                  `json:"currencyId,omitempty"`
	OwnerEmployeeID *int                  `json:"ownerEmployeeId,omitempty"`
	SalesRepID      *int                  `json:"salesRepId,omitempty"`
	Memo            string                `json:"memo"`
	Notes           string                `json:"notes"`
	InternalNotes   string                `json:"internalNotes"`
	CustomFields    map[string]any        `json:"customFields"`
	Lines           []CreditMemoLineInput `json:"lines"`
	Applications    []ApplicationInput    `json:"applications"`
}

// UpdateCreditMemoInput is the request payload for PATCH
// /api/tenant/credit-memos/{uuid}.
//
// Lines and money are only honored while the memo is DRFT (spec AD-15): once
// APPV the memo is an authorized instrument and applications may exist against
// it. Non-monetary fields stay editable in any non-terminal status.
type UpdateCreditMemoInput struct {
	ReferenceNumber string                `json:"referenceNumber"`
	CreditMemoDate  *time.Time            `json:"creditMemoDate,omitempty"`
	Reason          string                `json:"reason"`
	SalesTaxPercent *float64              `json:"salesTaxPercent,omitempty"`
	Adjustment      *float64              `json:"adjustment,omitempty"`
	PriceLevelID    *int                  `json:"priceLevelId,omitempty"`
	CurrencyID      *int                  `json:"currencyId,omitempty"`
	OwnerEmployeeID *int                  `json:"ownerEmployeeId,omitempty"`
	SalesRepID      *int                  `json:"salesRepId,omitempty"`
	Memo            string                `json:"memo"`
	Notes           string                `json:"notes"`
	InternalNotes   string                `json:"internalNotes"`
	CustomFields    map[string]any        `json:"customFields"`
	Lines           []CreditMemoLineInput `json:"lines,omitempty"`
}

// Page is one page of credit memos plus keyset-pagination state.
type Page struct {
	Records    []CreditMemo `json:"records"`
	NextCursor string       `json:"nextCursor"`
	HasMore    bool         `json:"hasMore"`
}
