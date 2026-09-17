// Package companylocation stores the physical addresses a tenant operates
// from (offices, warehouses, showrooms) — a distinct concept from
// companyprofile's billing/shipping/return addresses, which describe how
// documents route rather than where the business physically is. Configured
// from Configuration -> Company Info -> Locations.
package companylocation

import (
	"fmt"
	"strings"
)

// MaxFieldLength bounds every text field, matching the VARCHAR(255) column
// widths in the company_location table.
const MaxFieldLength = 255

// Address is a structured postal address — the same line1/line2/suite/city/
// country/state/zip shape companyprofile.Address and a CRM record's own
// address fields use.
type Address struct {
	Line1   string `json:"line1"`
	Line2   string `json:"line2"`
	Suite   string `json:"suite"`
	City    string `json:"city"`
	Country string `json:"country"`
	State   string `json:"state"`
	Zip     string `json:"zip"`
}

// Location is one saved location, as returned to the client.
type Location struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Phone     string  `json:"phone"`
	Address   Address `json:"address"`
	IsDefault bool    `json:"isDefault"`
}

// Input is the write shape for Create/Update — IsDefault is deliberately
// absent; SetDefault is the only way to move the default flag.
type Input struct {
	Name    string  `json:"name"`
	Phone   string  `json:"phone"`
	Address Address `json:"address"`
}

// addressFieldValues labels each sub-field of addr (e.g. "address.line1")
// for Validate's error messages.
func addressFieldValues(addr Address) map[string]string {
	return map[string]string{
		"address.line1":   addr.Line1,
		"address.line2":   addr.Line2,
		"address.suite":   addr.Suite,
		"address.city":    addr.City,
		"address.country": addr.Country,
		"address.state":   addr.State,
		"address.zip":     addr.Zip,
	}
}

// Validate checks an Input's bounds before it is persisted. Name is the only
// required field — phone and every address sub-field are optional.
func Validate(in Input) error {
	if strings.TrimSpace(in.Name) == "" {
		return ClientError{"name is required"}
	}
	fields := map[string]string{
		"name":  in.Name,
		"phone": in.Phone,
	}
	for label, value := range addressFieldValues(in.Address) {
		fields[label] = value
	}
	for field, value := range fields {
		if len(value) > MaxFieldLength {
			return ClientError{fmt.Sprintf("%s must be at most %d characters", field, MaxFieldLength)}
		}
	}
	return nil
}
