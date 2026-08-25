// controllers/vendorbill.go
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
	"stonesuite-backend/vendorbill"
)

// VendorBillOps handles the Vendor Bill endpoints: a dedicated relational
// module (header + line items + AD-6 approval + AD-7 settlement ledger),
// the accounts-payable mirror of Invoice -- a sibling of Purchase Order/Item
// Receipt, not served through the generic /api/tenant/crm/{workflowKey}
// JSONB router. Mirrors PurchaseOrderOps' auth/IDOR/error-mapping
// conventions.
//
// Routes:
//
//	GET    /api/tenant/vendor-bills                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/vendor-bills/search             — filter + sort + search + pagination
//	POST   /api/tenant/vendor-bills                    — create
//	GET    /api/tenant/vendor-bills/{uuid}             — get (+ items, + payments)
//	PATCH  /api/tenant/vendor-bills/{uuid}             — update (DRFT only)
//	DELETE /api/tenant/vendor-bills/{uuid}             — soft delete (DRFT/VOID only)
//	POST   /api/tenant/vendor-bills/{uuid}/transition  — status change
//	POST   /api/tenant/vendor-bills/{uuid}/approve     — approval sign-off
type VendorBillOps struct{}

// NewVendorBillOps constructs the handler group.
func NewVendorBillOps() *VendorBillOps { return &VendorBillOps{} }

// authVB resolves JWT + tenant pool + the vendor_bill:<action> RBAC grant
// for requests with no specific record yet (list/search/create).
func (h *VendorBillOps) authVB(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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

// authVBByUUID resolves auth for a single-record action, then enforces the
// row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *VendorBillOps) authVBByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *vendorbill.VendorBill, bool) {
	pool, identityID, scope, ok := h.authVB(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	bill, err := vendorbill.Get(r.Context(), pool, uuid)
	if errors.Is(err, vendorbill.ErrNotFound) {
		fail(w, http.StatusNotFound, "Vendor bill not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load vendor bill.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, bill.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", string(authz.ResourceVendorBill),
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Vendor bill not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, bill, true
}

// vbFail maps a store error to an HTTP response.
func vbFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, vendorbill.ErrNotFound):
		fail(w, http.StatusNotFound, "Vendor bill not found.")
	case errors.Is(err, vendorbill.ErrInvalidTransition),
		errors.Is(err, vendorbill.ErrApprovalRequired),
		errors.Is(err, vendorbill.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, vendorbill.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case vendorbill.IsClientError(err):
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

// List GET /api/tenant/vendor-bills
func (h *VendorBillOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVB(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/vendor-bills/search
func (h *VendorBillOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVB(w, r, authz.ActionRead)
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

func (h *VendorBillOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := vendorbill.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vbFail(w, err, "Failed to search vendor bills.")
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

// Create POST /api/tenant/vendor-bills
func (h *VendorBillOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authVB(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in vendorbill.CreateVendorBillInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	bill, err := vendorbill.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to create vendor bill.")
		return
	}
	auditVB(r, pool, identityID, "create", bill.ID, nil, bill)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorBill": bill})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/vendor-bills/{uuid}
func (h *VendorBillOps) Get(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, bill, ok := h.authVBByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		vbFail(w, err, "Failed to load vendor bill.")
		return
	}
	info, err := vendorbill.GetApprovalInfo(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		vbFail(w, err, "Failed to load vendor bill.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": bill, "approval": info})
}

// Update PATCH /api/tenant/vendor-bills/{uuid}
func (h *VendorBillOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authVBByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in vendorbill.UpdateVendorBillInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := vendorbill.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to update vendor bill.")
		return
	}
	auditVB(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": after})
}

// Delete DELETE /api/tenant/vendor-bills/{uuid}
func (h *VendorBillOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authVBByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := vendorbill.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		vbFail(w, err, "Failed to delete vendor bill.")
		return
	}
	auditVBDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Vendor bill deleted."})
}

// Transition POST /api/tenant/vendor-bills/{uuid}/transition  body {"toStatusCode":"..."}
func (h *VendorBillOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVBByUUID(w, r, uuid, authz.ActionTransition)
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
	bill, err := vendorbill.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to apply transition.")
		return
	}
	auditVB(r, pool, identityID, "transition", uuid, nil, bill)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": bill})
}

// Approve POST /api/tenant/vendor-bills/{uuid}/approve
func (h *VendorBillOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVBByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		vbFail(w, err, "Failed to approve vendor bill.")
		return
	}
	bill, err := vendorbill.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		if errors.Is(err, vendorbill.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		vbFail(w, err, "Failed to approve vendor bill.")
		return
	}
	auditVB(r, pool, identityID, "approve", uuid, nil, bill)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": bill})
}
