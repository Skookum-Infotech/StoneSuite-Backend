package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/companylocation"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// CompanyLocationOps serves Configuration -> Company Info -> Locations: the
// physical addresses a tenant operates from, distinct from
// CompanyProfileOps's billing/shipping/return addresses. Reuses the
// company_profile resource/actions rather than adding a new RBAC resource --
// this is the same page, and whoever can configure company info should
// manage locations too.
//
// Routes:
//
//	GET    /api/tenant/company-locations              — list
//	POST   /api/tenant/company-locations              — create
//	PATCH  /api/tenant/company-locations/{id}          — update
//	DELETE /api/tenant/company-locations/{id}          — soft delete
//	POST   /api/tenant/company-locations/{id}/default  — move the default
type CompanyLocationOps struct{}

// NewCompanyLocationOps constructs the handler group.
func NewCompanyLocationOps() *CompanyLocationOps { return &CompanyLocationOps{} }

// authorize resolves the caller, checks company_profile:action, and returns
// the tenant pool and the caller's identity id. On failure it writes the
// response and returns ok=false.
func (h *CompanyLocationOps) authorize(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceCompanyProfile, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceCompanyProfile), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" company locations.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// companyLocationFail maps a store error to an HTTP response.
func companyLocationFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, companylocation.ErrNotFound):
		fail(w, http.StatusNotFound, "Location not found.")
	case companylocation.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// List GET /api/tenant/company-locations
func (h *CompanyLocationOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authorize(w, r, authz.ActionRead)
	if !ok {
		return
	}
	items, err := companylocation.List(r.Context(), pool)
	if err != nil {
		companyLocationFail(w, err, "Failed to list company locations.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "locations": items})
}

// Create POST /api/tenant/company-locations
func (h *CompanyLocationOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var in companylocation.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	loc, err := companylocation.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		companyLocationFail(w, err, "Failed to create company location.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "location": loc})
}

// Update PATCH /api/tenant/company-locations/{id}
func (h *CompanyLocationOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var in companylocation.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := companylocation.Update(r.Context(), pool, r.PathValue("id"), in); err != nil {
		companyLocationFail(w, err, "Failed to update company location.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Location updated."})
}

// SetDefault POST /api/tenant/company-locations/{id}/default
func (h *CompanyLocationOps) SetDefault(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	if err := companylocation.SetDefault(r.Context(), pool, r.PathValue("id")); err != nil {
		companyLocationFail(w, err, "Failed to set the default location.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Default location updated."})
}

// Delete DELETE /api/tenant/company-locations/{id}
func (h *CompanyLocationOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	if err := companylocation.Delete(r.Context(), pool, r.PathValue("id"), resolveEmployeeID(r, identityID)); err != nil {
		companyLocationFail(w, err, "Failed to delete company location.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Location deleted."})
}
