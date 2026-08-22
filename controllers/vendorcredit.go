// controllers/vendorcredit.go
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
	"stonesuite-backend/vendorcredit"
)

// VendorCreditOps handles the Vendor Credit endpoints: a header-only,
// relational module (no lines, AD-1) — the accounts-payable mirror of
// CreditMemoOps, applying against vendor_bill instead of invoice. Mirrors
// CreditMemoOps' auth/IDOR/error-mapping conventions exactly.
type VendorCreditOps struct{}

// NewVendorCreditOps constructs the handler group.
func NewVendorCreditOps() *VendorCreditOps { return &VendorCreditOps{} }

// authVendorCredit resolves JWT + tenant pool + the vendor_credit:<action>
// RBAC grant for requests with no specific record yet (list/search/create).
func (h *VendorCreditOps) authVendorCredit(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceVendorCredit, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceVendorCredit), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" vendor credits.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authVendorCreditByUUID resolves auth for a single-record action, then
// enforces the row-level IDOR guard. Denial returns 404 (not 403) so callers
// cannot enumerate ids outside their scope.
func (h *VendorCreditOps) authVendorCreditByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authVendorCredit(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	vc, err := vendorcredit.Get(r.Context(), pool, uuid)
	if errors.Is(err, vendorcredit.ErrNotFound) {
		fail(w, http.StatusNotFound, "Vendor credit not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load vendor credit.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, vc.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceVendorCredit),
			"action", string(action), "scope", string(scope))
		// 404, not 403: a 403 would confirm the id exists and let it be enumerated.
		fail(w, http.StatusNotFound, "Vendor credit not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

// vendorBillInScopeForUpdate checks the caller holds vendor_bill:update and
// that the target vendor bill is within their scope, writing the response
// and returning false on denial (404 on scope denial, per the IDOR
// convention). Used by Apply/Reverse because those endpoints mutate a
// vendor bill's AP balance as a side effect of a vendor-credit-side action.
func (h *VendorCreditOps) vendorBillInScopeForUpdate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, billUUID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceVendorBill, authz.ActionUpdate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceVendorBill), "action", string(authz.ActionUpdate))
		fail(w, http.StatusForbidden, "You do not have permission to update vendor bills.")
		return false
	}
	if decision.Scope == authz.ScopeAll {
		return true
	}
	bill, err := vendorbill.Get(r.Context(), pool, billUUID)
	if errors.Is(err, vendorbill.ErrNotFound) {
		fail(w, http.StatusNotFound, "Vendor bill not found.")
		return false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load vendor bill.")
		return false
	}
	allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, identityID, bill.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", billUUID, "resource", string(authz.ResourceVendorBill),
			"action", "update", "scope", string(decision.Scope))
		fail(w, http.StatusNotFound, "Vendor bill not found.")
		return false
	}
	return true
}

// vendorCreditFail maps a store error to an HTTP response.
func vendorCreditFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, vendorcredit.ErrNotFound):
		fail(w, http.StatusNotFound, "Vendor credit not found.")
	case errors.Is(err, vendorcredit.ErrInvalidTransition),
		errors.Is(err, vendorcredit.ErrApprovalRequired),
		errors.Is(err, vendorcredit.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, vendorcredit.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	default:
		var ce vendorcredit.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		// Apply/Reverse reach into the vendorbill package for the row lock and
		// the AP rollup, so a bad vendor bill surfaces as vendorbill.ClientError.
		var ve vendorbill.ClientError
		if errors.As(err, &ve) {
			fail(w, http.StatusBadRequest, ve.Error())
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

// Create POST /api/tenant/vendor-credits
func (h *VendorCreditOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authVendorCredit(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in vendorcredit.CreateVendorCreditInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	vc, err := vendorcredit.Create(r.Context(), pool, in, empID)
	if err != nil {
		vendorCreditFail(w, err, "Failed to create vendor credit.")
		return
	}
	auditVC(r, pool, identityID, "create", vc.ID, nil, vc)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorCredit": vc})
}

// Get GET /api/tenant/vendor-credits/{uuid}
func (h *VendorCreditOps) Get(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorCreditByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	vc, err := vendorcredit.Get(r.Context(), pool, uuid)
	if err != nil {
		vendorCreditFail(w, err, "Failed to load vendor credit.")
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		vendorCreditFail(w, err, "Failed to load vendor credit.")
		return
	}
	info, err := vendorcredit.GetApprovalInfo(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		vendorCreditFail(w, err, "Failed to load vendor credit.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorCredit": vc, "approval": info})
}

// Approve POST /api/tenant/vendor-credits/{uuid}/approve — approval sign-off (AD-8).
// ActionApprove, not ActionTransition -- approving a vendor credit is what
// authorizes real credit against AP, a deliberately distinct capability
// from moving the record around (see authz/catalog.go).
func (h *VendorCreditOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorCreditByUUID(w, r, uuid, authz.ActionApprove)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		vendorCreditFail(w, err, "Failed to approve vendor credit.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	vc, err := vendorcredit.Approve(r.Context(), pool, uuid, empID, isSuperAdmin)
	if err != nil {
		if errors.Is(err, vendorcredit.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		vendorCreditFail(w, err, "Failed to approve vendor credit.")
		return
	}
	auditVC(r, pool, identityID, "approve", uuid, nil, vc)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorCredit": vc})
}

// Update PATCH /api/tenant/vendor-credits/{uuid}
func (h *VendorCreditOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorCreditByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in vendorcredit.UpdateVendorCreditInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := vendorcredit.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := vendorcredit.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		vendorCreditFail(w, err, "Failed to update vendor credit.")
		return
	}
	auditVC(r, pool, identityID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorCredit": after})
}

// Delete DELETE /api/tenant/vendor-credits/{uuid}
func (h *VendorCreditOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorCreditByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := vendorcredit.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := vendorcredit.SoftDelete(r.Context(), pool, id, empID); err != nil {
		vendorCreditFail(w, err, "Failed to delete vendor credit.")
		return
	}
	auditVCDelete(r, pool, identityID, id, before)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Vendor credit deleted."})
}

// List GET /api/tenant/vendor-credits
func (h *VendorCreditOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVendorCredit(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := vendorcredit.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vendorCreditFail(w, err, "Failed to list vendor credits.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Search POST /api/tenant/vendor-credits/search
func (h *VendorCreditOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVendorCredit(w, r, authz.ActionRead)
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
	page, err := vendorcredit.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vendorCreditFail(w, err, "Failed to search vendor credits.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
