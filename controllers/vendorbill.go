package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/vendorbill"
)

// VendorBillOps groups the vendor bill HTTP handlers.
type VendorBillOps struct{}

// NewVendorBillOps constructs a VendorBillOps.
func NewVendorBillOps() *VendorBillOps { return &VendorBillOps{} }

func (h *VendorBillOps) authVendorBill(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceVendorBill, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceVendorBill), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" vendor bills.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

func (h *VendorBillOps) authVendorBillByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authVendorBill(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	vb, err := vendorbill.Get(r.Context(), pool, uuid)
	if errors.Is(err, vendorbill.ErrNotFound) {
		fail(w, http.StatusNotFound, "Vendor bill not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load vendor bill.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, vb.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceVendorBill),
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Vendor bill not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

func vendorBillFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, vendorbill.ErrNotFound):
		fail(w, http.StatusNotFound, "Vendor bill not found.")
	case errors.Is(err, vendorbill.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	default:
		var ce vendorbill.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// Create handles POST /api/tenant/vendor-bills.
func (h *VendorBillOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authVendorBill(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in vendorbill.CreateVendorBillInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	vb, err := vendorbill.Create(r.Context(), pool, in, empID)
	if err != nil {
		vendorBillFail(w, err, "Failed to create vendor bill.")
		return
	}
	auditVendorBill(r, pool, empID, "create", vb.ID, nil, vb)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorBill": vb})
}

// Get handles GET /api/tenant/vendor-bills/{uuid}.
func (h *VendorBillOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authVendorBillByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	vb, err := vendorbill.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		vendorBillFail(w, err, "Failed to load vendor bill.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": vb})
}

// Update handles PATCH /api/tenant/vendor-bills/{uuid}.
func (h *VendorBillOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorBillByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in vendorbill.UpdateVendorBillInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := vendorbill.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := vendorbill.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		vendorBillFail(w, err, "Failed to update vendor bill.")
		return
	}
	auditVendorBill(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": after})
}

// Delete handles DELETE /api/tenant/vendor-bills/{uuid}.
func (h *VendorBillOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorBillByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := vendorbill.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := vendorbill.SoftDelete(r.Context(), pool, id, empID); err != nil {
		vendorBillFail(w, err, "Failed to delete vendor bill.")
		return
	}
	auditVendorBill(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Vendor bill deleted."})
}

// List handles GET /api/tenant/vendor-bills.
func (h *VendorBillOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVendorBill(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := vendorbill.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vendorBillFail(w, err, "Failed to list vendor bills.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Search handles POST /api/tenant/vendor-bills/search.
func (h *VendorBillOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVendorBill(w, r, authz.ActionRead)
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
	page, err := vendorbill.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vendorBillFail(w, err, "Failed to search vendor bills.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
