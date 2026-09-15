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

// Bounds for profile fields, matching the VARCHAR(255)/TEXT column widths in
// the company_profile table.
const (
	MaxShortFieldLength = 255  // company_name, legal_name, industry, website, country, currency, timezone, tax_id
	MaxAddressLength    = 5000 // billing_address, shipping_address, return_address
)

// Profile is the tenant's own company info — mirrors the field set captured
// (as free text, per-tenant-application only) during onboarding, but kept
// here as a proper per-tenant settings row editable at any time.
type Profile struct {
	CompanyName     string `json:"companyName"`
	LegalName       string `json:"legalName"`
	Industry        string `json:"industry"`
	Website         string `json:"website"`
	Country         string `json:"country"`
	Currency        string `json:"currency"`
	Timezone        string `json:"timezone"`
	TaxID           string `json:"taxId"`
	BillingAddress  string `json:"billingAddress"`
	ShippingAddress string `json:"shippingAddress"`
	ReturnAddress   string `json:"returnAddress"`
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

// Validate checks a profile's bounds before it is persisted. CompanyName is
// the only required field — every other onboarding-style field is optional.
func Validate(p Profile) error {
	if strings.TrimSpace(p.CompanyName) == "" {
		return ValidationError{"companyName is required"}
	}
	shortFields := map[string]string{
		"companyName": p.CompanyName,
		"legalName":   p.LegalName,
		"industry":    p.Industry,
		"website":     p.Website,
		"country":     p.Country,
		"currency":    p.Currency,
		"timezone":    p.Timezone,
		"taxId":       p.TaxID,
	}
	for field, value := range shortFields {
		if len(value) > MaxShortFieldLength {
			return ValidationError{fmt.Sprintf("%s must be at most %d characters", field, MaxShortFieldLength)}
		}
	}
	addressFields := map[string]string{
		"billingAddress":  p.BillingAddress,
		"shippingAddress": p.ShippingAddress,
		"returnAddress":   p.ReturnAddress,
	}
	for field, value := range addressFields {
		if len(value) > MaxAddressLength {
			return ValidationError{fmt.Sprintf("%s must be at most %d characters", field, MaxAddressLength)}
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
		SELECT company_name, legal_name, industry, website, country, currency,
		       timezone, tax_id, billing_address, shipping_address, return_address
		FROM company_profile WHERE id = 1`).
		Scan(&p.CompanyName, &p.LegalName, &p.Industry, &p.Website, &p.Country, &p.Currency,
			&p.Timezone, &p.TaxID, &p.BillingAddress, &p.ShippingAddress, &p.ReturnAddress)
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
			id, company_name, legal_name, industry, website, country, currency,
			timezone, tax_id, billing_address, shipping_address, return_address
		) VALUES (1,$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (id) DO UPDATE
			SET company_name     = EXCLUDED.company_name,
			    legal_name       = EXCLUDED.legal_name,
			    industry         = EXCLUDED.industry,
			    website          = EXCLUDED.website,
			    country          = EXCLUDED.country,
			    currency         = EXCLUDED.currency,
			    timezone         = EXCLUDED.timezone,
			    tax_id           = EXCLUDED.tax_id,
			    billing_address  = EXCLUDED.billing_address,
			    shipping_address = EXCLUDED.shipping_address,
			    return_address   = EXCLUDED.return_address,
			    updated_at       = NOW()`,
		p.CompanyName, p.LegalName, p.Industry, p.Website, p.Country, p.Currency,
		p.Timezone, p.TaxID, p.BillingAddress, p.ShippingAddress, p.ReturnAddress)
	if err != nil {
		return fmt.Errorf("upsert company profile: %w", err)
	}
	return nil
}
