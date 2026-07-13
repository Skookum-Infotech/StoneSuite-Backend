package inventory

import "time"

// Item represents an inventory item with all its attributes.
type Item struct {
	ID           string         `json:"id"`
	SKU          string         `json:"sku"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	UnitID       int            `json:"unitId"`
	UnitPrice    float64        `json:"unitPrice"`
	CurrencyID   *int           `json:"currencyId,omitempty"`
	TaxRateID    *int           `json:"taxRateId,omitempty"`
	IsActive     bool           `json:"isActive"`
	CustomFields map[string]any `json:"customFields"`
	CreatedAt    time.Time      `json:"createdAt"`
	UpdatedAt    time.Time      `json:"updatedAt"`
}

// CreateItemInput represents the input data for creating a new inventory item.
type CreateItemInput struct {
	SKU          string         `json:"sku"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	UnitID       int            `json:"unitId"`
	UnitPrice    float64        `json:"unitPrice"`
	CurrencyID   *int           `json:"currencyId,omitempty"`
	TaxRateID    *int           `json:"taxRateId,omitempty"`
	CustomFields map[string]any `json:"customFields"`
}
