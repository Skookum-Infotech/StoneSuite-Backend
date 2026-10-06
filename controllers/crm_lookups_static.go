package controllers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/cache"
)

const (
	// crmStaticLookupTTL bounds staleness of the seeded reference vocabularies.
	// Nothing writes them at runtime (they change only via seed/migration), so
	// the TTL is the whole invalidation story.
	crmStaticLookupTTL = 5 * time.Minute
	crmStaticLookupMax = 2000
)

// lookupLoadError carries the user-facing message for a failed vocabulary load.
type lookupLoadError struct {
	msg string
	err error
}

func (e *lookupLoadError) Error() string { return e.msg + ": " + e.err.Error() }
func (e *lookupLoadError) Unwrap() error { return e.err }

// crmStaticLookups is the tenant-wide, caller-independent part of the CRM
// lookups response. Callers must treat it as read-only (it is shared).
type crmStaticLookups struct {
	CustomerTypes []LookupItem
	ArStatuses []LookupItem
	PaymentTerms []LookupItem
	Currencies []CurrencyLookupItem
	Countries []LookupItem
	LeadSources []LookupItem
	ContactMethods []LookupItem
	PriceLevels []LookupItem
	States []StateLookupItem
	RecordTypes []LookupItem
	CrmStatuses []LookupItem
}

func newCRMStaticCache() *cache.TTLCache[string, *crmStaticLookups] {
	return cache.NewWithMax[string, *crmStaticLookups](crmStaticLookupTTL, crmStaticLookupMax)
}

// loadCRMStaticLookups reads every seeded reference vocabulary from the tenant DB.
func loadCRMStaticLookups(ctx context.Context, pool *pgxpool.Pool) (*crmStaticLookups, error) {
	out := &crmStaticLookups{}
	var err error
	if out.CustomerTypes, err = queryLookupItems(ctx, pool,
		`SELECT customer_type_id, customer_type_code, customer_type_name FROM lkp_customer_type
		 WHERE customer_type_is_active AND customer_type_deleted_at IS NULL ORDER BY customer_type_name`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load customer types.", err: err}
	}
	if out.ArStatuses, err = queryLookupItems(ctx, pool,
		`SELECT customer_ar_status_id, customer_ar_status_code, customer_ar_status_name FROM lkp_customer_ar_status
		 WHERE customer_ar_status_is_active AND customer_ar_status_deleted_at IS NULL ORDER BY customer_ar_status_name`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load AR statuses.", err: err}
	}
	if out.PaymentTerms, err = queryLookupItems(ctx, pool,
		`SELECT payment_terms_id, payment_terms_code, payment_terms_name FROM lkp_payment_terms
		 WHERE payment_terms_is_active AND payment_terms_deleted_at IS NULL ORDER BY payment_terms_name`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load payment terms.", err: err}
	}
	if out.Currencies, err = queryCurrencyLookupItems(ctx, pool); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load currencies.", err: err}
	}
	if out.Countries, err = queryLookupItems(ctx, pool,
		`SELECT country_id, country_code2, country_name FROM lkp_country
		 WHERE country_is_active AND country_deleted_at IS NULL ORDER BY country_name`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load countries.", err: err}
	}
	if out.LeadSources, err = queryLookupItems(ctx, pool,
		`SELECT lead_source_id, '', lead_source_name FROM lkp_crm_lead_source
		 WHERE lead_source_is_active AND lead_source_deleted_at IS NULL ORDER BY lead_source_name`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load lead sources.", err: err}
	}
	if out.ContactMethods, err = queryLookupItems(ctx, pool,
		`SELECT contact_method_id, '', contact_method_name FROM lkp_contact_method
		 WHERE contact_method_is_active AND contact_method_deleted_at IS NULL ORDER BY contact_method_name`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load contact methods.", err: err}
	}
	if out.PriceLevels, err = queryLookupItems(ctx, pool,
		`SELECT price_level_id, price_level_code, price_level_name FROM lkp_price_level
		 WHERE price_level_is_active AND price_level_deleted_at IS NULL ORDER BY price_level_id`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load price levels.", err: err}
	}
	if out.States, err = queryStateLookupItems(ctx, pool); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load states.", err: err}
	}
	if out.RecordTypes, err = queryLookupItems(ctx, pool,
		`SELECT record_type_id, record_type_code, record_type_name FROM lkp_record_type
		 WHERE record_type_is_active AND record_type_deleted_at IS NULL ORDER BY record_type_name`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load record types.", err: err}
	}
	if out.CrmStatuses, err = queryLookupItems(ctx, pool,
		`SELECT crm_status_id, crm_status_code, crm_status_name FROM lkp_crm_status
		 WHERE crm_status_is_active AND crm_status_deleted_at IS NULL ORDER BY crm_status_name`); err != nil {
		return nil, &lookupLoadError{msg: "Failed to load CRM statuses.", err: err}
	}
	return out, nil
}
