package salesorder

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

// LineInput2 is one ordered line on create/update. InventoryItemUUID selects
// a catalog item (the server snapshots its sku/name/description/unit/price/
// tax, ignoring SKU/ItemName/UnitCode/TaxPercent below); omit it for a
// free-text line, in which case Description is required and SKU/ItemName/
// UnitCode/TaxPercent (when set) are taken as-is instead of derived.
type LineInput2 struct {
	LineNumber        int      `json:"lineNumber"`
	InventoryItemUUID string   `json:"inventoryItemUuid"`
	Description       string   `json:"description"`
	SKU               string   `json:"sku"`
	ItemName          string   `json:"itemName"`
	UnitCode          string   `json:"unitCode"`
	Quantity          float64  `json:"quantity"`
	UnitPrice         float64  `json:"unitPrice"`
	DiscountPercent   float64  `json:"discountPercent"`
	TaxRateID         *int     `json:"taxRateId"`
	TaxPercent        *float64 `json:"taxPercent"`
	WarehouseID       *int     `json:"warehouseId"`
}

// orderFields is the header payload shared by create and update (everything
// except the customer, which is fixed at creation and never changes).
type orderFields struct {
	PONumber           string         `json:"poNumber"`
	ReferenceNumber    string         `json:"referenceNumber"`
	OrderDate          string         `json:"orderDate"`        // "yyyy-mm-dd"
	ExpectedDelivery   string         `json:"expectedDelivery"` // "yyyy-mm-dd"
	PaymentDueDate     string         `json:"paymentDueDate"`   // "yyyy-mm-dd"; blank ⇒ derived from terms (AD-8)
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
	Items              []LineInput2   `json:"items"`
}

// CreateOrderInput is the create-request payload (spec §10).
type CreateOrderInput struct {
	CustomerUUID string `json:"customerUuid"`
	orderFields
}

// UpdateOrderInput mirrors CreateOrderInput minus the customer (an order's
// customer is fixed after creation).
type UpdateOrderInput struct {
	orderFields
}

// CustomerRef is the light customer reference on an Order response.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Line is one ordered line in the API response — the frozen snapshot values
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

	// Fulfillment (schema.org OrderItem.orderItemStatus, AD-9). FulfilledQuantity
	// is the stored rollup of the line's allocations; Status is derived from it.
	FulfilledQuantity float64 `json:"fulfilledQuantity"`
	Status            string  `json:"status"` // open | partial | filled
}

// lineStatus derives the schema.org-style per-line fulfillment status (AD-9)
// as a pure function of fulfilled vs ordered quantity — never stored, so it
// cannot drift from line_fulfilled_quantity.
func lineStatus(fulfilled, quantity float64) string {
	switch {
	case fulfilled <= 0:
		return "open"
	case fulfilled >= quantity:
		return "filled"
	default:
		return "partial"
	}
}

// Order is the full API response for a sales order header (+ lines, when
// loaded by Get). OwnerUserID backs the controller's IDOR scope check and is
// never serialized. Every field the create/update contract (orderFields)
// accepts round-trips back here too, so the Edit page can reload an order and
// re-save it without silently blanking billing/shipping or any header field.
type Order struct {
	ID               string      `json:"id"`
	Number           string      `json:"salesOrderNumber"`
	Status           string      `json:"status"`         // human label, e.g. "Draft"
	StatusCode       string      `json:"statusCode"`     // lkp_record_status code, e.g. "DRFT"
	ApprovalStatus   string      `json:"approvalStatus"` // none | pending | approved (AD-10)
	Customer         CustomerRef `json:"customer"`
	OwnerUserID      string      `json:"-"`
	OrderDate        string      `json:"orderDate"`
	ExpectedDelivery string      `json:"expectedDelivery,omitempty"`
	PaymentDueDate   string      `json:"paymentDueDate,omitempty"`
	PONumber         string      `json:"poNumber,omitempty"`
	ReferenceNumber  string      `json:"referenceNumber,omitempty"`
	Memo             string      `json:"memo,omitempty"`
	Notes            string      `json:"notes,omitempty"`
	InternalNotes    string      `json:"internalNotes,omitempty"`
	TermsConditions  string      `json:"termsConditions,omitempty"`

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

// Page is one page of a keyset-paginated order search. List rows omit Items
// (search selects header columns only, to avoid an N+1 line-item join) —
// only Get loads the full order with lines.
type Page struct {
	Records    []Order
	NextCursor string
	HasMore    bool
}
