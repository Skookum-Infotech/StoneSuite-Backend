// Package companyprofile stores the tenant's own company identity and
// address (as opposed to a CRM Lead/Prospect/Customer's address, or a
// vendor's) — a single row per tenant database, edited from Configuration →
// Company Info.
package companyprofile

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxFieldLength bounds every profile field, matching the VARCHAR(255)
// column widths in the company_profile table.
const MaxFieldLength = 255

// Address is a structured postal address — the same line1/line2/suite/city/
// country/state/zip shape CRM Lead/Prospect/Customer records use for their
// own Primary/Billing/Shipping addresses (see crmFields.ts's *_addr_*
// fields), so the tenant's own company address is captured the same way as
// any other address in the app instead of one free-text blob.
type Address struct {
	Line1   string `json:"line1"`
	Line2   string `json:"line2"`
	Suite   string `json:"suite"`
	City    string `json:"city"`
	Country string `json:"country"`
	State   string `json:"state"`
	Zip     string `json:"zip"`
}

// Profile is the tenant's own company info — mirrors the field set captured
// (as free text, per-tenant-application only) during onboarding, but kept
// here as a proper per-tenant settings row editable at any time.
type Profile struct {
	CompanyName string `json:"companyName"`
	LegalName   string `json:"legalName"`
	Industry    string `json:"industry"`
	Website     string `json:"website"`
	Country     string `json:"country"`
	Currency    string `json:"currency"`
	Timezone    string `json:"timezone"`
	TaxID       string `json:"taxId"`
	LogoKey     string `json:"logoKey"`

	BillingAddress  Address `json:"billingAddress"`
	ShippingAddress Address `json:"shippingAddress"`
	ReturnAddress   Address `json:"returnAddress"`
}

// Querier is the subset of pgx behavior Get/Upsert need (consumer-side
// interface, per Go Rules). A *pgxpool.Pool or a pgx.Tx both satisfy it.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ValidationError is returned by Validate/Upsert when the profile itself
// fails a validation rule — the controller maps this (via errors.As) to 400,
// unlike any other error out of Upsert (a genuine DB failure), which maps to
// 500.
type ValidationError struct{ msg string }

func (e ValidationError) Error() string { return e.msg }

// addressFieldValues labels each sub-field of addr under label (e.g.
// "billingAddress.line1") for Validate's error messages.
func addressFieldValues(label string, addr Address) map[string]string {
	return map[string]string{
		label + ".line1":   addr.Line1,
		label + ".line2":   addr.Line2,
		label + ".suite":   addr.Suite,
		label + ".city":    addr.City,
		label + ".country": addr.Country,
		label + ".state":   addr.State,
		label + ".zip":     addr.Zip,
	}
}

// Validate checks a profile's bounds before it is persisted. CompanyName is
// the only required field — every other onboarding-style field, including
// every address sub-field, is optional.
func Validate(p Profile) error {
	if strings.TrimSpace(p.CompanyName) == "" {
		return ValidationError{"companyName is required"}
	}
	fields := map[string]string{
		"companyName": p.CompanyName,
		"legalName":   p.LegalName,
		"industry":    p.Industry,
		"website":     p.Website,
		"country":     p.Country,
		"currency":    p.Currency,
		"timezone":    p.Timezone,
		"taxId":       p.TaxID,
	}
	for label, value := range addressFieldValues("billingAddress", p.BillingAddress) {
		fields[label] = value
	}
	for label, value := range addressFieldValues("shippingAddress", p.ShippingAddress) {
		fields[label] = value
	}
	for label, value := range addressFieldValues("returnAddress", p.ReturnAddress) {
		fields[label] = value
	}
	for field, value := range fields {
		if len(value) > MaxFieldLength {
			return ValidationError{fmt.Sprintf("%s must be at most %d characters", field, MaxFieldLength)}
		}
	}
	return nil
}

// Get loads the tenant's company profile. If none has been saved yet, it
// returns a zero-value Profile (not an error) — the frontend renders that as
// an empty form.
func Get(ctx context.Context, q Querier) (*Profile, error) {
	p := &Profile{}
	err := q.QueryRow(ctx, `
		SELECT company_name, legal_name, industry, website, country, currency, timezone, tax_id, logo_r2_key,
		       billing_addr_line1, billing_addr_line2, billing_addr_suite, billing_addr_city, billing_addr_country, billing_addr_state, billing_addr_zip,
		       shipping_addr_line1, shipping_addr_line2, shipping_addr_suite, shipping_addr_city, shipping_addr_country, shipping_addr_state, shipping_addr_zip,
		       return_addr_line1, return_addr_line2, return_addr_suite, return_addr_city, return_addr_country, return_addr_state, return_addr_zip
		FROM company_profile WHERE id = 1`).
		Scan(
			&p.CompanyName, &p.LegalName, &p.Industry, &p.Website, &p.Country, &p.Currency, &p.Timezone, &p.TaxID, &p.LogoKey,
			&p.BillingAddress.Line1, &p.BillingAddress.Line2, &p.BillingAddress.Suite, &p.BillingAddress.City, &p.BillingAddress.Country, &p.BillingAddress.State, &p.BillingAddress.Zip,
			&p.ShippingAddress.Line1, &p.ShippingAddress.Line2, &p.ShippingAddress.Suite, &p.ShippingAddress.City, &p.ShippingAddress.Country, &p.ShippingAddress.State, &p.ShippingAddress.Zip,
			&p.ReturnAddress.Line1, &p.ReturnAddress.Line2, &p.ReturnAddress.Suite, &p.ReturnAddress.City, &p.ReturnAddress.Country, &p.ReturnAddress.State, &p.ReturnAddress.Zip,
		)
	if err == pgx.ErrNoRows {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get company profile: %w", err)
	}
	return p, nil
}

// Upsert validates and saves the tenant's company profile.
func Upsert(ctx context.Context, q Querier, p Profile) error {
	if err := Validate(p); err != nil {
		return err
	}
	_, err := q.Exec(ctx, `
		INSERT INTO company_profile (
			id, company_name, legal_name, industry, website, country, currency, timezone, tax_id,
			billing_addr_line1, billing_addr_line2, billing_addr_suite, billing_addr_city, billing_addr_country, billing_addr_state, billing_addr_zip,
			shipping_addr_line1, shipping_addr_line2, shipping_addr_suite, shipping_addr_city, shipping_addr_country, shipping_addr_state, shipping_addr_zip,
			return_addr_line1, return_addr_line2, return_addr_suite, return_addr_city, return_addr_country, return_addr_state, return_addr_zip
		) VALUES (
			1,$1,$2,$3,$4,$5,$6,$7,$8,
			$9,$10,$11,$12,$13,$14,$15,
			$16,$17,$18,$19,$20,$21,$22,
			$23,$24,$25,$26,$27,$28,$29
		)
		ON CONFLICT (id) DO UPDATE
			SET company_name         = EXCLUDED.company_name,
			    legal_name           = EXCLUDED.legal_name,
			    industry             = EXCLUDED.industry,
			    website              = EXCLUDED.website,
			    country              = EXCLUDED.country,
			    currency             = EXCLUDED.currency,
			    timezone             = EXCLUDED.timezone,
			    tax_id               = EXCLUDED.tax_id,
			    billing_addr_line1   = EXCLUDED.billing_addr_line1,
			    billing_addr_line2   = EXCLUDED.billing_addr_line2,
			    billing_addr_suite   = EXCLUDED.billing_addr_suite,
			    billing_addr_city    = EXCLUDED.billing_addr_city,
			    billing_addr_country = EXCLUDED.billing_addr_country,
			    billing_addr_state   = EXCLUDED.billing_addr_state,
			    billing_addr_zip     = EXCLUDED.billing_addr_zip,
			    shipping_addr_line1   = EXCLUDED.shipping_addr_line1,
			    shipping_addr_line2   = EXCLUDED.shipping_addr_line2,
			    shipping_addr_suite   = EXCLUDED.shipping_addr_suite,
			    shipping_addr_city    = EXCLUDED.shipping_addr_city,
			    shipping_addr_country = EXCLUDED.shipping_addr_country,
			    shipping_addr_state   = EXCLUDED.shipping_addr_state,
			    shipping_addr_zip     = EXCLUDED.shipping_addr_zip,
			    return_addr_line1   = EXCLUDED.return_addr_line1,
			    return_addr_line2   = EXCLUDED.return_addr_line2,
			    return_addr_suite   = EXCLUDED.return_addr_suite,
			    return_addr_city    = EXCLUDED.return_addr_city,
			    return_addr_country = EXCLUDED.return_addr_country,
			    return_addr_state   = EXCLUDED.return_addr_state,
			    return_addr_zip     = EXCLUDED.return_addr_zip,
			    updated_at = NOW()`,
		p.CompanyName, p.LegalName, p.Industry, p.Website, p.Country, p.Currency, p.Timezone, p.TaxID,
		p.BillingAddress.Line1, p.BillingAddress.Line2, p.BillingAddress.Suite, p.BillingAddress.City, p.BillingAddress.Country, p.BillingAddress.State, p.BillingAddress.Zip,
		p.ShippingAddress.Line1, p.ShippingAddress.Line2, p.ShippingAddress.Suite, p.ShippingAddress.City, p.ShippingAddress.Country, p.ShippingAddress.State, p.ShippingAddress.Zip,
		p.ReturnAddress.Line1, p.ReturnAddress.Line2, p.ReturnAddress.Suite, p.ReturnAddress.City, p.ReturnAddress.Country, p.ReturnAddress.State, p.ReturnAddress.Zip,
	)
	if err != nil {
		return fmt.Errorf("upsert company profile: %w", err)
	}
	return nil
}

// SetLogoKey stores (or clears, with an empty string) the tenant's logo R2
// object key without touching any other company_profile field -- kept
// separate from Upsert so the logo endpoints never need to round-trip the
// rest of the profile just to change the logo.
func SetLogoKey(ctx context.Context, q Querier, key string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO company_profile (id, logo_r2_key) VALUES (1, $1)
		ON CONFLICT (id) DO UPDATE SET logo_r2_key = EXCLUDED.logo_r2_key, updated_at = NOW()`,
		key)
	if err != nil {
		return fmt.Errorf("set company profile logo key: %w", err)
	}
	return nil
}
