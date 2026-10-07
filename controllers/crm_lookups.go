package controllers

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/cache"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
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
type CRMLookups struct {
	// statics caches the seeded vocabularies per tenant; employees and parent
	// customers are caller-scoped and volatile, so they are always read live.
	statics *cache.TTLCache[string, *crmStaticLookups]
}

// NewCRMLookups constructs the handler group.
func NewCRMLookups() *CRMLookups { return &CRMLookups{statics: newCRMStaticCache()} }

// static returns the tenant's cached seeded vocabularies, loading them once
// per TTL (concurrent misses share one load). A request with no resolved
// tenant bypasses the cache.
func (h *CRMLookups) static(ctx context.Context, pool *pgxpool.Pool) (*crmStaticLookups, error) {
	t, err := tenancy.TenantFromContext(ctx)
	if err != nil || h.statics == nil {
		return loadCRMStaticLookups(ctx, pool)
	}
	return h.statics.GetOrLoad(t.ID, func() (*crmStaticLookups, error) { return loadCRMStaticLookups(ctx, pool) })
}

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

// CurrencyLookupItem additionally carries the display symbol (e.g. "$", "€")
// so the frontend can render amounts without hardcoding or duplicating a
// currency prefix.
type CurrencyLookupItem struct {
	ID     int    `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Symbol string `json:"symbol"`
}

// GetLookups GET /api/tenant/crm/lookups
func (h *CRMLookups) GetLookups(w http.ResponseWriter, r *http.Request) {
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	ctx := r.Context()

	// RBAC: this endpoint backs the core-field selects behind the Lead /
	// Prospect / Customer forms, so require read on at least one of them — the
	// same gate as GET /api/tenant/crm/statuses. Without it the whole reference
	// set (including the customer book and staff directory below) was readable
	// by any authenticated tenant user, including a zero-grant guest.
	payload, err := middleware.GetUserFromContext(ctx)
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	crmDecision, err := authz.CheckAny(ctx, pool, payload.ID,
		[]authz.Resource{authz.ResourceLead, authz.ResourceProspect, authz.ResourceCustomer}, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !crmDecision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", "crm_lookups", "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read CRM records.")
		return
	}

	static, err := h.static(ctx, pool)
	if err != nil {
		var le *lookupLoadError
		if errors.As(err, &le) {
			fail(w, http.StatusInternalServerError, le.msg)
		} else {
			fail(w, http.StatusInternalServerError, "Failed to load lookups.")
		}
		return
	}
	// Employees: maps employee_id (integer FK) to display name, used for the
	// Sales Rep / Customer Owner fields on every document module (estimate,
	// quote, sales order, invoice, credit memo, ...) and any other employee FK
	// select. Filtered to staff whose user holds create/update permission on a
	// CRM resource (customer, lead, prospect) so unrelated employees
	// (accounting, warehouse, etc.) never show up as sales rep candidates.
	// This is directory data (a name, not account/security details), so
	// listing it follows the same rule as the other reference lookups above
	// (customerTypes, currencies, ...): available to any caller who already
	// cleared this endpoint's crmDecision check, no extra gate. It previously
	// also required user:read (the tenant user-management permission from
	// controllers/user.go) which no ordinary Sales Rep role holds, so the
	// picker silently degraded to empty for exactly the roles that needed it.
	employees, err := queryEligibleSalesRepEmployees(ctx, pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load employees.")
		return
	}
	// Parent customers: used for the Parent Customer (customer_parent_company) FK
	// select, which stores the integer customer_id of the owning company record.
	// Scope-filtered to the caller's CRM scope exactly as ListRecords is, so an
	// own/team-scoped caller sees only customers they own, not the whole book.
	parentCustomers := []LookupItem{}
	loadParents := true
	parentQuery := `SELECT c.customer_id, COALESCE(c.customer_doc_num,''), c.customer_name
		 FROM customer c
		 JOIN lkp_record_type rt ON rt.record_type_id = c.record_type
		 WHERE rt.record_type_code IN ('LEAD','PROS','CUST')
		   AND c.customer_deleted_at IS NULL
		   AND c.customer_name IS NOT NULL`
	parentArgs := []any{}
	if crmDecision.Scope == authz.ScopeOwn {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, payload.ID)
		if found {
			parentQuery += ` AND c.customer_crm_owner_user_id = $1`
			parentArgs = append(parentArgs, empID)
		} else {
			// No employee profile → owns nothing (mirrors ListRecords).
			loadParents = false
		}
	}
	if loadParents {
		parentQuery += ` ORDER BY c.customer_name`
		parentCustomers, err = queryLookupItems(ctx, pool, parentQuery, parentArgs...)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load parent customers.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"lookups": map[string]any{
			"customerTypes":   static.CustomerTypes,
			"arStatuses":      static.ArStatuses,
			"paymentTerms":    static.PaymentTerms,
			"currencies":      static.Currencies,
			"countries":       static.Countries,
			"states":          static.States,
			"leadSources":     static.LeadSources,
			"contactMethods":  static.ContactMethods,
			"priceLevels":     static.PriceLevels,
			"recordTypes":     static.RecordTypes,
			"crmStatuses":     static.CrmStatuses,
			"employees":       employees,
			"parentCustomers": parentCustomers,
		},
	})
}

func queryLookupItems(ctx context.Context, pool *pgxpool.Pool, query string, args ...any) ([]LookupItem, error) {
	rows, err := pool.Query(ctx, query, args...)
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

func queryCurrencyLookupItems(ctx context.Context, pool *pgxpool.Pool) ([]CurrencyLookupItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT currency_id, currency_code, currency_name, currency_symbol FROM lkp_currency
		WHERE currency_is_active AND currency_deleted_at IS NULL ORDER BY currency_name`)
	if err != nil {
		return nil, fmt.Errorf("query currencies: %w", err)
	}
	defer rows.Close()
	out := []CurrencyLookupItem{}
	for rows.Next() {
		var item CurrencyLookupItem
		if err := rows.Scan(&item.ID, &item.Code, &item.Name, &item.Symbol); err != nil {
			return nil, fmt.Errorf("scan currency: %w", err)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// queryEligibleSalesRepEmployees returns active employees whose linked user
// holds create or update permission on a CRM resource (customer, lead,
// prospect), i.e. staff who actually work CRM records — not every employee
// in the tenant.
func queryEligibleSalesRepEmployees(ctx context.Context, pool *pgxpool.Pool) ([]LookupItem, error) {
	return queryLookupItems(ctx, pool,
		`SELECT e.employee_id, '', COALESCE(NULLIF(u.full_name,''), u.email)
		 FROM employee e
		 JOIN users u ON u.id = e.employee_user_id
		 WHERE e.employee_deleted_at IS NULL
		   AND e.employee_is_active
		   AND u.status = 'active'
		   AND EXISTS (
		     SELECT 1
		     FROM user_roles ur
		     JOIN role_permissions rp ON rp.role_id = ur.role_id
		     WHERE ur.user_id = u.id
		       AND rp.resource IN ('customer', 'lead', 'prospect', '*')
		       AND rp.action IN ('create', 'update', '*')
		   )
		 ORDER BY COALESCE(NULLIF(u.full_name,''), u.email)`)
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
