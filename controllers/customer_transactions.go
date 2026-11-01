// controllers/customer_transactions.go
package controllers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/automation"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/customertransactions"
	"stonesuite-backend/dateonly"
	"stonesuite-backend/middleware"
)

// customerRecordKey is the CRM workflow key a record must have for its
// Transactions tab to exist; any other key answers 404.
const customerRecordKey = "customer"

// customerTxResource names this endpoint in security-event logs.
const customerTxResource = "customer-transactions"

// CustomerTransactionsOps serves a customer's Transactions tab: every document
// tied to the customer across the modules the caller may read. The parent
// customer is resolved and IDOR-guarded via CRMOps.authCRMByRecordID
// (customer:read); each document type is then gated by its own grant inside
// the customertransactions package.
type CustomerTransactionsOps struct {
	crm        *CRMOps
	fallbackTZ *time.Location
}

// NewCustomerTransactionsOps constructs the handler group; fallback is the
// timezone used for tenants that have not set one (nil means UTC).
func NewCustomerTransactionsOps(fallback *time.Location) *CustomerTransactionsOps {
	return &CustomerTransactionsOps{crm: NewCRMOps(), fallbackTZ: fallback}
}

// authCustomer resolves the parent record and requires it to be a customer
// before any permission check, so a caller lacking lead/prospect read cannot
// tell a lead id from a missing one (both 404). It then runs the shared
// customer:read + ownership-scope IDOR guard.
func (h *CustomerTransactionsOps) authCustomer(w http.ResponseWriter, r *http.Request, recordID string) (*pgxpool.Pool, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	st, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	key, err := st.KeyForRecord(r.Context(), pool, recordID)
	if errors.Is(err, crmstore.ErrRecordNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return nil, "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return nil, "", false
	}
	if key != customerRecordKey {
		logSecurityEvent(r, "idor_denied",
			"identity", payload.ID, "record", recordID, "resource", customerTxResource,
			"action", string(authz.ActionRead))
		fail(w, http.StatusNotFound, "Record not found.")
		return nil, "", false
	}
	_, pool, _, identityID, ok := h.crm.authCRMByRecordID(w, r, recordID, authz.ActionRead)
	if !ok {
		return nil, "", false
	}
	return pool, identityID, true
}

// tenantToday returns the tenant-local calendar date, from the company profile
// timezone with the configured fallback when it is unset or unreadable.
func (h *CustomerTransactionsOps) tenantToday(ctx context.Context, pool *pgxpool.Pool) time.Time {
	tz := ""
	prof, err := companyprofile.Get(ctx, pool)
	if err != nil {
		slog.Warn("customer transactions: company profile unavailable, using fallback timezone", "error", err)
	} else if prof != nil {
		tz = prof.Timezone
	}
	return automation.TenantToday(time.Now(), tz, h.fallbackTZ)
}

// writeCustomerTxError maps a package error to its HTTP response and reports
// whether it handled one.
func writeCustomerTxError(w http.ResponseWriter, err error) bool {
	var invalid *customertransactions.InvalidParamError
	switch {
	case err == nil:
		return false
	case errors.As(err, &invalid):
		fail(w, http.StatusBadRequest, "Invalid "+invalid.Param+": "+invalid.Msg+".")
	case errors.Is(err, customertransactions.ErrCustomerNotFound):
		fail(w, http.StatusNotFound, "Record not found.")
	default:
		slog.Error("customer transactions: request failed", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load customer transactions.")
	}
	return true
}

// parseCustomerTxParams reads the list query string. It writes the 400 and
// returns false on a malformed value.
func parseCustomerTxParams(w http.ResponseWriter, r *http.Request, p *customertransactions.Params) bool {
	q := r.URL.Query()
	p.Type, p.Status, p.Search = q.Get("type"), q.Get("status"), q.Get("q")
	for _, d := range []struct {
		name string
		dst  **dateonly.Date
	}{{"from", &p.From}, {"to", &p.To}} {
		v := q.Get(d.name)
		if v == "" {
			continue
		}
		parsed, err := dateonly.Parse(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "Invalid "+d.name+": must be a date (YYYY-MM-DD).")
			return false
		}
		*d.dst = &parsed
	}
	if v := q.Get("open"); v != "" {
		open, err := strconv.ParseBool(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "Invalid open: must be true or false.")
			return false
		}
		p.OpenOnly = open
	}
	for _, n := range []struct {
		name string
		dst  *int
	}{{"page", &p.Page}, {"limit", &p.Limit}} {
		v := q.Get(n.name)
		if v == "" {
			continue
		}
		i, err := strconv.Atoi(v)
		if err != nil || i < 0 {
			fail(w, http.StatusBadRequest, "Invalid "+n.name+": must be a non-negative integer.")
			return false
		}
		*n.dst = i
	}
	return true
}

// List GET /api/tenant/crm/customer/records/{id}/transactions
//
// Query params (all optional): type, status, from, to (yyyy-mm-dd), q, open,
// page, limit.
func (h *CustomerTransactionsOps) List(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, identityID, ok := h.authCustomer(w, r, recordID)
	if !ok {
		return
	}
	params := customertransactions.Params{IdentityID: identityID, CustomerUUID: recordID}
	if !parseCustomerTxParams(w, r, &params) {
		return
	}
	params.Today = h.tenantToday(r.Context(), pool)

	page, err := customertransactions.List(r.Context(), pool, params)
	if writeCustomerTxError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"rows":    page.Rows,
		"total":   page.Total,
		"page":    page.Page,
		"limit":   page.Limit,
	})
}

// Summary GET /api/tenant/crm/customer/records/{id}/transactions/summary
//
// The unfiltered receivables figures, per-type counts and readable types.
func (h *CustomerTransactionsOps) Summary(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, identityID, ok := h.authCustomer(w, r, recordID)
	if !ok {
		return
	}
	ov, err := customertransactions.GetOverview(r.Context(), pool, identityID, recordID, h.tenantToday(r.Context(), pool))
	if writeCustomerTxError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"scopeLimited": ov.ScopeLimited,
		"summary":      ov.Summary,
		"counts":       ov.Counts,
		"types":        ov.Types,
	})
}
