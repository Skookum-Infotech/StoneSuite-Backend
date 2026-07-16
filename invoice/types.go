package invoice

import "time"

// CustomerRef is the flattened {id, name} for "current customer" navigation.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SalesOrderRef is the flattened {id, number} for lineage navigation.
type SalesOrderRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

// Address is the billing or shipping snapshot.
type Address struct {
	CustomerName string `json:"customerName"`
	Attention    string `json:"attention"`
	AddrLine1    string `json:"addrLine1"`
	AddrLine2    string `json:"addrLine2"`
	SuiteUnit    string `json:"suiteUnit"`
	City         string `json:"city"`
	StateID      *int   `json:"stateId,omitempty"`
	Zip          string `json:"zip"`
	CountryID    *int   `json:"countryId,omitempty"`
	Phone        string `json:"phone"`
	Fax          string `json:"fax"`
	Email        string `json:"email"`
}

// AddressInput is the create/update payload shape for a billing or shipping block.
type AddressInput = Address

// Line is one invoice_item row: catalog/free-text snapshot + stored money.
type Line struct {
	ID               string  `json:"id"`
	LineNumber       int     `json:"lineNumber"`
	InventoryItemID  *string `json:"inventoryItemId,omitempty"`
	SalesOrderItemID *string `json:"salesOrderItemId,omitempty"`
	SKU              string  `json:"sku"`
	ItemName         string  `json:"itemName"`
	Description      string  `json:"description"`
	UnitID           *int    `json:"unitId,omitempty"`
	UnitCode         string  `json:"unitCode"`
	Quantity         float64 `json:"quantity"`
	UnitPrice        float64 `json:"unitPrice"`
	DiscountPercent  float64 `json:"discountPercent"`
	TaxRateID        *int    `json:"taxRateId,omitempty"`
	TaxPercent       float64 `json:"taxPercent"`
	LineSubtotal     float64 `json:"lineSubtotal"`
	LineDiscount     float64 `json:"lineDiscount"`
	LineTax          float64 `json:"lineTax"`
	LineTotal        float64 `json:"lineTotal"`
}

// Invoice is the invoice header + its lines.
type Invoice struct {
	ID     string `json:"id"`
	Number string `json:"invoiceNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	Customer   CustomerRef    `json:"customer"`
	SalesOrder *SalesOrderRef `json:"salesOrder,omitempty"` // nullable lineage

	OwnerUserID        string `json:"-"`
	OwnerEmployeeID    *int   `json:"ownerEmployeeId,omitempty"`
	SalesRepEmployeeID *int   `json:"salesRepEmployeeId,omitempty"`

	PONumber        string `json:"poNumber"`
	ReferenceNumber string `json:"referenceNumber"`
	InvoiceDate     string `json:"invoiceDate"`       // "yyyy-mm-dd"
	DueDate         string `json:"dueDate,omitempty"` // "yyyy-mm-dd"

	PaymentTermsID *int    `json:"paymentTermsId,omitempty"`
	PriceLevelID   *int    `json:"priceLevelId,omitempty"`
	CurrencyID     *int    `json:"currencyId,omitempty"`
	ExchangeRate   float64 `json:"exchangeRate"`

	SalesTaxPercent float64 `json:"salesTaxPercent"`
	Memo            string  `json:"memo"`
	Notes           string  `json:"notes"`
	InternalNotes   string  `json:"internalNotes"`
	TermsConditions string  `json:"termsConditions"`

	Subtotal       float64 `json:"subtotal"`
	DiscountTotal  float64 `json:"discountTotal"`
	TaxTotal       float64 `json:"taxTotal"`
	ShippingCharge float64 `json:"shippingCharge"`
	Adjustment     float64 `json:"adjustment"`
	GrandTotal     float64 `json:"grandTotal"`

	AmountPaid float64 `json:"amountPaid"`
	BalanceDue float64 `json:"balanceDue"`

	ShipSameAsBilling bool    `json:"shipSameAsBilling"`
	Billing           Address `json:"billing"`
	Shipping          Address `json:"shipping"`

	CustomFields map[string]any `json:"customFields"`
	Items        []Line         `json:"items"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// InvoiceLineInput is one line of a create/update request.
type InvoiceLineInput struct {
	LineNumber        int     `json:"lineNumber"`
	InventoryItemUUID string  `json:"inventoryItemUuid,omitempty"`
	Description       string  `json:"description,omitempty"`
	Quantity          float64 `json:"quantity"`
	UnitPrice         float64 `json:"unitPrice"`
	DiscountPercent   float64 `json:"discountPercent"`
	TaxRateID         *int    `json:"taxRateId,omitempty"`
}

// CreateInvoiceInput is the request payload for POST /api/tenant/invoices.
// Notice it doesn't take salesOrderUuid (that is only set via the convert endpoint).
type CreateInvoiceInput struct {
	CustomerUUID       string             `json:"customerUuid"`
	PONumber           string             `json:"poNumber"`
	ReferenceNumber    string             `json:"referenceNumber"`
	InvoiceDate        string             `json:"invoiceDate"`       // "yyyy-mm-dd"; blank ⇒ CURRENT_DATE
	DueDate            string             `json:"dueDate,omitempty"` // "yyyy-mm-dd"
	PaymentTermsID     *int               `json:"paymentTermsId,omitempty"`
	PriceLevelID       *int               `json:"priceLevelId,omitempty"`
	CurrencyID         *int               `json:"currencyId,omitempty"`
	SalesRepEmployeeID *int               `json:"salesRepEmployeeId,omitempty"`
	OwnerEmployeeID    *int               `json:"ownerEmployeeId,omitempty"`
	SalesTaxPercent    float64            `json:"salesTaxPercent"`
	Memo               string             `json:"memo"`
	Notes              string             `json:"notes"`
	InternalNotes      string             `json:"internalNotes"`
	TermsConditions    string             `json:"termsConditions"`
	ShipSameAsBilling  bool               `json:"shipSameAsBilling"`
	Billing            AddressInput       `json:"billing"`
	Shipping           AddressInput       `json:"shipping"`
	ShippingCharge     float64            `json:"shippingCharge"`
	Adjustment         float64            `json:"adjustment"`
	CustomFields       map[string]any     `json:"customFields"`
	Items              []InvoiceLineInput `json:"items"`
}

// UpdateInvoiceInput is the request payload for PATCH /api/tenant/invoices/{uuid}.
type UpdateInvoiceInput struct {
	PONumber           string             `json:"poNumber"`
	ReferenceNumber    string             `json:"referenceNumber"`
	InvoiceDate        string             `json:"invoiceDate"`       // "yyyy-mm-dd"; blank leaves the stored date unchanged
	DueDate            string             `json:"dueDate,omitempty"` // "yyyy-mm-dd"
	PaymentTermsID     *int               `json:"paymentTermsId,omitempty"`
	PriceLevelID       *int               `json:"priceLevelId,omitempty"`
	CurrencyID         *int               `json:"currencyId,omitempty"`
	SalesRepEmployeeID *int               `json:"salesRepEmployeeId,omitempty"`
	OwnerEmployeeID    *int               `json:"ownerEmployeeId,omitempty"`
	SalesTaxPercent    float64            `json:"salesTaxPercent"`
	Memo               string             `json:"memo"`
	Notes              string             `json:"notes"`
	InternalNotes      string             `json:"internalNotes"`
	TermsConditions    string             `json:"termsConditions"`
	ShipSameAsBilling  bool               `json:"shipSameAsBilling"`
	Billing            AddressInput       `json:"billing"`
	Shipping           AddressInput       `json:"shipping"`
	ShippingCharge     float64            `json:"shippingCharge"`
	Adjustment         float64            `json:"adjustment"`
	CustomFields       map[string]any     `json:"customFields"`
	Items              []InvoiceLineInput `json:"items"`
}

// Page is one page of invoices plus keyset-pagination state.
type Page struct {
	Records    []Invoice `json:"records"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}
