package controllers

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/tenancy"
)

// CRMLookups serves the read-only lkp_* reference tables that back the
// unified CRM core-field selects (customer type, AR status, payment terms,
// currency, country, state, lead source, contact method, price level). The
// same 12 lkp_* tables exist for every tenant regardless of design_version,
// so this endpoint is design-agnostic.
//
// Routes:
//
//	GET /api/tenant/crm/lookups
type CRMLookups struct{}

// NewCRMLookups constructs the handler group.
func NewCRMLookups() *CRMLookups { return &CRMLookups{} }

// LookupItem is a generic {id, code, name} reference row.
type LookupItem struct {
	ID   int    `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// StateLookupItem additionally carries the owning country id for client-side
// filtering of the state select when a country is chosen.
type StateLookupItem struct {
	ID        int    `json:"id"`
	Code      string `json:"code"`
	Name      string `json:"name"`
	CountryID int    `json:"countryId"`
}

// GetLookups GET /api/tenant/crm/lookups
func (h *CRMLookups) GetLookups(w http.ResponseWriter, r *http.Request) {
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	ctx := r.Context()

	customerTypes, err := queryLookupItems(ctx, pool,
		`SELECT customer_type_id, customer_type_code, customer_type_name FROM lkp_customer_type
		 WHERE customer_type_is_active AND customer_type_deleted_at IS NULL ORDER BY customer_type_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load customer types.")
		return
	}
	arStatuses, err := queryLookupItems(ctx, pool,
		`SELECT customer_ar_status_id, customer_ar_status_code, customer_ar_status_name FROM lkp_customer_ar_status
		 WHERE customer_ar_status_is_active AND customer_ar_status_deleted_at IS NULL ORDER BY customer_ar_status_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load AR statuses.")
		return
	}
	paymentTerms, err := queryLookupItems(ctx, pool,
		`SELECT payment_terms_id, payment_terms_code, payment_terms_name FROM lkp_payment_terms
		 WHERE payment_terms_is_active AND payment_terms_deleted_at IS NULL ORDER BY payment_terms_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load payment terms.")
		return
	}
	currencies, err := queryLookupItems(ctx, pool,
		`SELECT currency_id, currency_code, currency_name FROM lkp_currency
		 WHERE currency_is_active AND currency_deleted_at IS NULL ORDER BY currency_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load currencies.")
		return
	}
	countries, err := queryLookupItems(ctx, pool,
		`SELECT country_id, country_code2, country_name FROM lkp_country
		 WHERE country_is_active AND country_deleted_at IS NULL ORDER BY country_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load countries.")
		return
	}
	leadSources, err := queryLookupItems(ctx, pool,
		`SELECT lead_source_id, '', lead_source_name FROM lkp_crm_lead_source
		 WHERE lead_source_is_active AND lead_source_deleted_at IS NULL ORDER BY lead_source_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load lead sources.")
		return
	}
	contactMethods, err := queryLookupItems(ctx, pool,
		`SELECT contact_method_id, '', contact_method_name FROM lkp_contact_method
		 WHERE contact_method_is_active AND contact_method_deleted_at IS NULL ORDER BY contact_method_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load contact methods.")
		return
	}
	priceLevels, err := queryLookupItems(ctx, pool,
		`SELECT price_level_id, price_level_code, price_level_name FROM lkp_price_level
		 WHERE price_level_is_active AND price_level_deleted_at IS NULL ORDER BY price_level_id`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load price levels.")
		return
	}
	states, err := queryStateLookupItems(ctx, pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load states.")
		return
	}
	recordTypes, err := queryLookupItems(ctx, pool,
		`SELECT record_type_id, record_type_code, record_type_name FROM lkp_record_type
		 WHERE record_type_is_active AND record_type_deleted_at IS NULL ORDER BY record_type_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record types.")
		return
	}
	crmStatuses, err := queryLookupItems(ctx, pool,
		`SELECT crm_status_id, crm_status_code, crm_status_name FROM lkp_crm_status
		 WHERE crm_status_is_active AND crm_status_deleted_at IS NULL ORDER BY crm_status_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load CRM statuses.")
		return
	}
	// Employees: maps employee_id (integer FK) to display name, used for the
	// Sales Rep field and any other employee FK selects.
	employees, err := queryLookupItems(ctx, pool,
		`SELECT e.employee_id, '', COALESCE(NULLIF(u.full_name,''), u.email)
		 FROM employee e
		 JOIN users u ON u.id = e.employee_user_id
		 WHERE e.employee_deleted_at IS NULL AND u.status = 'active'
		 ORDER BY COALESCE(NULLIF(u.full_name,''), u.email)`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load employees.")
		return
	}
	// Parent customers: used for the Parent Customer (customer_parent_company) FK
	// select, which stores the integer customer_id of the owning company record.
	parentCustomers, err := queryLookupItems(ctx, pool,
		`SELECT c.customer_id, COALESCE(c.customer_doc_num,''), c.customer_name
		 FROM customer c
		 JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		 WHERE rt.record_type_code IN ('LEAD','PROS','CUST')
		   AND c.customer_deleted_at IS NULL
		   AND c.customer_name IS NOT NULL
		 ORDER BY c.customer_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load parent customers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"lookups": map[string]any{
			"customerTypes":   customerTypes,
			"arStatuses":      arStatuses,
			"paymentTerms":    paymentTerms,
			"currencies":      currencies,
			"countries":       countries,
			"states":          states,
			"leadSources":     leadSources,
			"contactMethods":  contactMethods,
			"priceLevels":     priceLevels,
			"recordTypes":     recordTypes,
			"crmStatuses":     crmStatuses,
			"employees":       employees,
			"parentCustomers": parentCustomers,
		},
	})
}

func queryLookupItems(ctx context.Context, pool *pgxpool.Pool, query string) ([]LookupItem, error) {
	rows, err := pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query lookup items: %w", err)
	}
	defer rows.Close()
	out := []LookupItem{}
	for rows.Next() {
		var item LookupItem
		if err := rows.Scan(&item.ID, &item.Code, &item.Name); err != nil {
			return nil, fmt.Errorf("scan lookup item: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func queryStateLookupItems(ctx context.Context, pool *pgxpool.Pool) ([]StateLookupItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT state_id, state_code, state_name, state_country_id FROM lkp_state
		WHERE state_is_active AND state_deleted_at IS NULL ORDER BY state_name`)
	if err != nil {
		return nil, fmt.Errorf("query states: %w", err)
	}
	defer rows.Close()
	out := []StateLookupItem{}
	for rows.Next() {
		var item StateLookupItem
		if err := rows.Scan(&item.ID, &item.Code, &item.Name, &item.CountryID); err != nil {
			return nil, fmt.Errorf("scan state: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}
