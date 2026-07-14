package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/vendors"
)

// VendorOps handles the Vendor endpoints: a dedicated relational module (a
// supplier/contractor directory modeled on schema.org Person ∩
// Organization), a sibling of the CRM customer table and Sales Order — not
// served through the generic /api/tenant/crm/{workflowKey} JSONB router.
// Mirrors SalesOrderOps' auth/IDOR/error-mapping conventions.
//
// Routes:
//
//	GET    /api/tenant/vendors                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/vendors/search              — filter + sort + search + pagination
//	POST   /api/tenant/vendors                     — create
//	GET    /api/tenant/vendors/{uuid}              — get
//	PATCH  /api/tenant/vendors/{uuid}              — update
//	DELETE /api/tenant/vendors/{uuid}              — soft delete
//	POST   /api/tenant/vendors/{uuid}/transition   — status change
//	GET    /api/tenant/vendors/{uuid}/audit        — audit trail
type VendorOps struct{}

// NewVendorOps constructs the handler group.
func NewVendorOps() *VendorOps { return &VendorOps{} }

// authVendor resolves JWT + tenant pool + the vendor:<action> RBAC grant for
// requests with no specific record yet (list/search/create).
func (h *VendorOps) authVendor(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceVendor, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" vendors.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authVendorByUUID resolves auth for a single-record action, then enforces
// the row-level IDOR guard: an own/team-scoped caller may only act on
// vendors they own. Denial returns 404 (not 403) so callers cannot enumerate
// ids outside their scope — mirrors authSOByUUID. Vendor has no team column
// (like Sales Order), so team scope behaves like own.
func (h *VendorOps) authVendorByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *vendors.Vendor, bool) {
	pool, identityID, scope, ok := h.authVendor(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	v, err := vendors.Get(r.Context(), pool, uuid)
	if errors.Is(err, vendors.ErrNotFound) {
		fail(w, http.StatusNotFound, "Vendor not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load vendor.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, v.OwnerUserID, "")
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", "vendor",
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Vendor not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, v, true
}

// vendorFail maps a store error to an HTTP response.
func vendorFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, vendors.ErrNotFound):
		fail(w, http.StatusNotFound, "Vendor not found.")
	case errors.Is(err, vendors.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	case vendors.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// ---- list / search / create --------------------------------------------------

// List GET /api/tenant/vendors — the unfiltered default list, built from
// query params (?limit=&cursor=&search=) rather than a JSON body.
func (h *VendorOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVendor(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/vendors/search — full filter + sort + global
// search + keyset pagination, composed onto the caller's RBAC scope.
func (h *VendorOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVendor(w, r, authz.ActionRead)
	if !ok {
		return
	}
	var req query.Request
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	h.search(w, r, pool, identityID, scope, req)
}

func (h *VendorOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := vendors.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vendorFail(w, err, "Failed to search vendors.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"scope":      scope,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// Create POST /api/tenant/vendors
func (h *VendorOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authVendor(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in vendors.CreateVendorInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	v, err := vendors.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		vendorFail(w, err, "Failed to create vendor.")
		return
	}
	auditVendor(r, pool, identityID, "create", v.ID, nil, v)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendor": v})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/vendors/{uuid}
func (h *VendorOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, v, ok := h.authVendorByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendor": v})
}

// Update PATCH /api/tenant/vendors/{uuid}
func (h *VendorOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authVendorByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in vendors.UpdateVendorInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := vendors.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		vendorFail(w, err, "Failed to update vendor.")
		return
	}
	auditVendor(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendor": after})
}

// Delete DELETE /api/tenant/vendors/{uuid}
func (h *VendorOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authVendorByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := vendors.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		vendorFail(w, err, "Failed to delete vendor.")
		return
	}
	auditVendorDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Vendor deleted."})
}

// Transition POST /api/tenant/vendors/{uuid}/transition  body {"toStatusCode":"..."}
func (h *VendorOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		ToStatusCode string `json:"toStatusCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	v, err := vendors.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		vendorFail(w, err, "Failed to apply transition.")
		return
	}
	auditVendor(r, pool, identityID, "transition", uuid, nil, v)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendor": v})
}
