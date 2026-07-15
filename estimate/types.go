package estimate

import "time"

// AddressInput is a billing or shipping snapshot block on create/update. All
// fields are optional; Create fills gaps from the referenced customer's
// matching address (spec AD-4) when the caller does not override them.
type AddressInput struct {
	CustomerName string `json:"customerName"`
	Attention    string `json:"attention"`
	AddrLine1    string `json:"addrLine1"`
	AddrLine2    string `json:"addrLine2"`
	SuiteUnit    string `json:"suiteUnit"`
	City         string `json:"city"`
	StateID      *int   `json:"stateId"`
	CountryID    *int   `json:"countryId"`
	Zip          string `json:"zip"`
	Phone        string `json:"phone"`
	Fax          string `json:"fax"`
	Email        string `json:"email"`
}

// LineInput is one estimated line on create/update. InventoryItemUUID selects
// a catalog item (the server snapshots its sku/name/description/unit/price/
// tax); omit it for a free-text line, in which case Description is required.
type LineInput struct {
	LineNumber        int     `json:"lineNumber"`
	InventoryItemUUID string  `json:"inventoryItemUuid"`
	Description       string  `json:"description"`
	Quantity          float64 `json:"quantity"`
	UnitPrice         float64 `json:"unitPrice"`
	DiscountPercent   float64 `json:"discountPercent"`
	TaxRateID         *int    `json:"taxRateId"`
}

// estimateFields is the header payload shared by create and update (everything
// except the customer, which is fixed at creation and never changes).
type estimateFields struct {
	PONumber           string         `json:"poNumber"`
	ReferenceNumber    string         `json:"referenceNumber"`
	EstimateDate       string         `json:"estimateDate"` // "yyyy-mm-dd"
	ValidUntil         string         `json:"validUntil"`   // "yyyy-mm-dd"
	PaymentTermsID     *int           `json:"paymentTermsId"`
	PriceLevelID       *int           `json:"priceLevelId"`
	CurrencyID         *int           `json:"currencyId"`
	SalesRepEmployeeID *int           `json:"salesRepEmployeeId"`
	OwnerEmployeeID    *int           `json:"ownerEmployeeId"`
	SalesTaxPercent    float64        `json:"salesTaxPercent"`
	Memo               string         `json:"memo"`
	Notes              string         `json:"notes"`
	InternalNotes      string         `json:"internalNotes"`
	TermsConditions    string         `json:"termsConditions"`
	ShipSameAsBilling  bool           `json:"shipSameAsBilling"`
	Billing            AddressInput   `json:"billing"`
	Shipping           AddressInput   `json:"shipping"`
	ShippingCharge     float64        `json:"shippingCharge"`
	Adjustment         float64        `json:"adjustment"`
	CustomFields       map[string]any `json:"customFields"`
	Items              []LineInput    `json:"items"`
}

// CreateEstimateInput is the create-request payload (spec §10).
type CreateEstimateInput struct {
	CustomerUUID string `json:"customerUuid"`
	estimateFields
}

// UpdateEstimateInput mirrors CreateEstimateInput minus the customer (an
// estimate's customer is fixed after creation).
type UpdateEstimateInput struct {
	estimateFields
}

// CustomerRef is the light customer reference on an Estimate response.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Line is one estimated line in the API response — the frozen snapshot values
// (spec AD-4), not live inventory_item data.
type Line struct {
	ID              string  `json:"id"`
	LineNumber      int     `json:"lineNumber"`
	InventoryItemID *string `json:"inventoryItemId,omitempty"`
	SKU             string  `json:"sku"`
	ItemName        string  `json:"itemName"`
	Description     string  `json:"description"`
	UnitCode        string  `json:"unitCode"`
	Quantity        float64 `json:"quantity"`
	UnitPrice       float64 `json:"unitPrice"`
	DiscountPercent float64 `json:"discountPercent"`
	TaxPercent      float64 `json:"taxPercent"`
	LineSubtotal    float64 `json:"lineSubtotal"`
	LineDiscount    float64 `json:"lineDiscount"`
	LineTax         float64 `json:"lineTax"`
	LineTotal       float64 `json:"lineTotal"`
}

// Estimate is the full API response for an estimate header (+ lines, when
// loaded by Get). OwnerUserID backs the controller's IDOR scope check and is
// never serialized.
type Estimate struct {
	ID              string      `json:"id"`
	Number          string      `json:"estimateNumber"`
	Status          string      `json:"status"`         // human label, e.g. "Draft"
	StatusCode      string      `json:"statusCode"`     // lkp_record_status code, e.g. "DRFT"
	ApprovalStatus  string      `json:"approvalStatus"` // none | pending | approved
	Customer        CustomerRef `json:"customer"`
	OwnerUserID     string      `json:"-"`
	EstimateDate    string      `json:"estimateDate"`
	ValidUntil      string      `json:"validUntil,omitempty"`
	PONumber        string      `json:"poNumber,omitempty"`
	ReferenceNumber string      `json:"referenceNumber,omitempty"`
	Memo            string      `json:"memo,omitempty"`
	Notes           string      `json:"notes,omitempty"`
	InternalNotes   string      `json:"internalNotes,omitempty"`
	TermsConditions string      `json:"termsConditions,omitempty"`

	PaymentTermsID     *int    `json:"paymentTermsId"`
	PriceLevelID       *int    `json:"priceLevelId"`
	CurrencyID         *int    `json:"currencyId"`
	SalesRepEmployeeID *int    `json:"salesRepEmployeeId"`
	OwnerEmployeeID    *int    `json:"ownerEmployeeId"`
	SalesTaxPercent    float64 `json:"salesTaxPercent"`

	ShipSameAsBilling bool         `json:"shipSameAsBilling"`
	Billing           AddressInput `json:"billing"`
	Shipping          AddressInput `json:"shipping"`

	CustomFields map[string]any `json:"customFields,omitempty"`

	Subtotal       float64   `json:"subtotal"`
	DiscountTotal  float64   `json:"discountTotal"`
	TaxTotal       float64   `json:"taxTotal"`
	ShippingCharge float64   `json:"shippingCharge"`
	Adjustment     float64   `json:"adjustment"`
	GrandTotal     float64   `json:"grandTotal"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Items          []Line    `json:"items,omitempty"`
}

// Page is one page of a keyset-paginated estimate search. List rows omit
// Items (search selects header columns only, to avoid an N+1 line-item join)
// — only Get loads the full estimate with lines.
type Page struct {
	Records    []Estimate
	NextCursor string
	HasMore    bool
}
