package proposedentity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"stonesuite-backend/docextract"
	"stonesuite-backend/proposedaction"
)

// Field length limits, in runes (email in bytes), matching the tenant schema
// column widths (customer_name 100, vendor_legal_name 150, inventory_item_name
// 150, inventory_item_sku 50, *_email 100, customer_tax_id 50).
const (
	maxSourceRef    = 100
	maxCustomerName = 100
	maxVendorName   = 150
	maxItemName     = 150
	maxEmail        = 100
	maxTaxID        = 50
	maxAddress      = 300
	maxSKU          = 50
	maxDescription  = 500
	maxDocUoM       = 20
	maxUnitCode     = 10
	// maxPriceCents caps a price at $1,000,000,000.00.
	maxPriceCents = int64(100_000_000_000)
	centsPerUnit  = 100
	noLimit       = 0
)

// sourceRefPattern is "<uuid>:<key>".
var sourceRefPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}:[A-Za-z0-9_.-]+$`)

// CustomerPayload is the proposal payload for a new customer. Address is
// shown to the reviewer but never persisted.
type CustomerPayload struct {
	SourceRef    string `json:"sourceRef"`
	Name         string `json:"name"`
	ContactEmail string `json:"contactEmail"`
	TaxID        string `json:"taxId,omitempty"`
	Address      string `json:"address,omitempty"`
}

// VendorPayload is the proposal payload for a new vendor. TaxID is preview
// only; Address is persisted as the vendor's physical address.
type VendorPayload struct {
	SourceRef string `json:"sourceRef"`
	Name      string `json:"name"`
	Email     string `json:"email,omitempty"`
	Address   string `json:"address,omitempty"`
	TaxID     string `json:"taxId,omitempty"`
}

// ItemPayload is the proposal payload for a new inventory item.
type ItemPayload struct {
	SourceRef              string `json:"sourceRef"`
	SKU                    string `json:"sku"`
	Name                   string `json:"name"`
	Description            string `json:"description,omitempty"`
	UnitID                 int    `json:"unitId"`
	UnitCode               string `json:"unitCode,omitempty"`
	DocUoM                 string `json:"docUoM,omitempty"`
	UnitPriceCents         int64  `json:"unitPriceCents"`
	PurchaseUnitPriceCents int64  `json:"purchaseUnitPriceCents"`
}

// cleanText strips control/format/bidi runes, collapses whitespace and clips
// to maxRunes runes (no limit when maxRunes is 0).
func cleanText(s string, maxRunes int) string { return docextract.Clean(s, maxRunes) }

// invalid builds a ValidationError; msg must never contain payload values.
func invalid(format string, args ...any) error {
	return &proposedaction.ValidationError{Message: fmt.Sprintf(format, args...)}
}

// strictDecode decodes one JSON object into dst, rejecting unknown fields and
// trailing data. The returned error never echoes payload content.
func strictDecode[T CustomerPayload | VendorPayload | ItemPayload](raw json.RawMessage, dst *T) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return invalid("payload is not a valid object for this action")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return invalid("payload has trailing data")
	}
	return nil
}

// checkText validates one string field. field is a code constant, never a value.
func checkText(field, s string, maxRunes int, required bool) error {
	return checkTextMasked(field, s, maxRunes, required, true)
}

// checkIdent validates an identifier-like value (SKU, unit code, address of an
// email). Unlike free text it is not rejected for matching a card/IBAN shape:
// a 13-digit barcode used as a SKU legitimately passes the Luhn check.
func checkIdent(field, s string, maxRunes int, required bool) error {
	return checkTextMasked(field, s, maxRunes, required, false)
}

// checkTextMasked is checkText with the sensitive-data guard switchable.
func checkTextMasked(field, s string, maxRunes int, required, guardSensitive bool) error {
	t := strings.TrimSpace(s)
	if t == "" {
		if required {
			return invalid("%s is required", field)
		}
		return nil
	}
	for _, r := range t {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return invalid("%s contains invalid characters", field)
		}
	}
	if utf8.RuneCountInString(t) > maxRunes {
		return invalid("%s is too long", field)
	}
	if guardSensitive && (t != docextract.MaskSensitive(t) || strings.Contains(t, docextract.MaskedPlaceholder)) {
		return invalid("%s looks like sensitive data", field)
	}
	return nil
}

// checkEmail validates a bare address (no display name).
func checkEmail(field, s string, required bool) error {
	if err := checkIdent(field, s, maxEmail, required); err != nil {
		return err
	}
	t := strings.TrimSpace(s)
	if t == "" {
		return nil
	}
	addr, err := mail.ParseAddress(t)
	if err != nil || addr.Address != t || len(t) > maxEmail {
		return invalid("%s is not a valid email address", field)
	}
	return nil
}

// checkSourceRef validates the "<uuid>:<key>" linkage token.
func checkSourceRef(s string) error {
	if s == "" {
		return invalid("sourceRef is required")
	}
	if len(s) > maxSourceRef || !sourceRefPattern.MatchString(s) {
		return invalid("sourceRef is malformed")
	}
	return nil
}

// centsToDollars converts integer cents to a float for the inventory API.
func centsToDollars(c int64) float64 { return float64(c) / centsPerUnit }

// formatDollars renders cents as "$12.34" for previews.
func formatDollars(c int64) string { return fmt.Sprintf("$%d.%02d", c/centsPerUnit, c%centsPerUnit) }
