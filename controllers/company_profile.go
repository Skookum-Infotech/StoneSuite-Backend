package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// CompanyProfileOps groups the tenant-scoped Company Info handlers -- the
// tenant's own company name/address (Configuration -> Company Info), kept
// distinct from a CRM Lead/Prospect/Customer's address or a vendor's.
type CompanyProfileOps struct{}

// NewCompanyProfileOps constructs the handler group.
func NewCompanyProfileOps() *CompanyProfileOps { return &CompanyProfileOps{} }

// authorize resolves the caller, checks company_profile:action, and returns
// the tenant pool. On failure it writes the response and returns ok=false.
func (h *CompanyProfileOps) authorize(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceCompanyProfile, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceCompanyProfile), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" the company profile.")
		return nil, false
	}
	return pool, true
}

// GetProfile GET /api/tenant/company-profile
func (h *CompanyProfileOps) GetProfile(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionRead)
	if !ok {
		return
	}
	profile, err := companyprofile.Get(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load company profile.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "companyProfile": profile})
}

// UpdateProfile PUT /api/tenant/company-profile
func (h *CompanyProfileOps) UpdateProfile(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var profile companyprofile.Profile
	if err := json.NewDecoder(r.Body).Decode(&profile); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := companyprofile.Upsert(r.Context(), pool, profile); err != nil {
		var ve companyprofile.ValidationError
		if errors.As(err, &ve) {
			fail(w, http.StatusBadRequest, ve.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to save company profile.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "companyProfile": profile})
}
