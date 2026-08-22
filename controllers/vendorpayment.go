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
	"stonesuite-backend/vendorpayment"
)

// VendorPaymentOps holds the HTTP handlers for the vendor payment module.
type VendorPaymentOps struct{}

// NewVendorPaymentOps constructs a VendorPaymentOps.
func NewVendorPaymentOps() *VendorPaymentOps { return &VendorPaymentOps{} }

// authVendorPayment resolves the caller, the tenant pool, and checks
// vendor_payment:action, logging and failing 403 on denial.
func (h *VendorPaymentOps) authVendorPayment(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceVendorPayment, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceVendorPayment), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" vendor payments.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authVendorPaymentByUUID additionally enforces the IDOR guard on a single
// vendor payment, returning 404 (not 403) on scope denial.
func (h *VendorPaymentOps) authVendorPaymentByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authVendorPayment(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	vp, err := vendorpayment.Get(r.Context(), pool, uuid)
	if errors.Is(err, vendorpayment.ErrNotFound) {
		fail(w, http.StatusNotFound, "Vendor payment not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load vendor payment.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, vp.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceVendorPayment),
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Vendor payment not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

// vendorBillInScopeForUpdate checks the caller holds vendor_bill:update and
// that the target vendor bill is within their scope, writing the response
// and returning false on denial (404 on scope denial, per the IDOR
// convention). Used by Apply/Unapply/Refund/RemoveRefund because those
// endpoints mutate a vendor bill's balance as a side effect of a vendor
// payment-side action (spec AD-10).
func (h *VendorPaymentOps) vendorBillInScopeForUpdate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, vendorBillUUID string) bool {
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
	vb, err := vendorbill.Get(r.Context(), pool, vendorBillUUID)
	if errors.Is(err, vendorbill.ErrNotFound) {
		fail(w, http.StatusNotFound, "Vendor bill not found.")
		return false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load vendor bill.")
		return false
	}
	allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, identityID, vb.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", vendorBillUUID, "resource", string(authz.ResourceVendorBill),
			"action", "update", "scope", string(decision.Scope))
		fail(w, http.StatusNotFound, "Vendor bill not found.")
		return false
	}
	return true
}

// vendorPaymentFail maps a vendorpayment/vendorbill error to the correct
// HTTP status and writes the JSON error response.
func vendorPaymentFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, vendorpayment.ErrNotFound):
		fail(w, http.StatusNotFound, "Vendor payment not found.")
	case errors.Is(err, vendorpayment.ErrInvalidTransition),
		errors.Is(err, vendorpayment.ErrApprovalRequired),
		errors.Is(err, vendorpayment.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, vendorpayment.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	default:
		var ce vendorpayment.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		// Apply/Refund reach into the vendorbill package for the row lock and
		// rollup, so a bad bill surfaces as vendorbill.ClientError. Without this
		// arm it would fall through to 500.
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

// Create handles POST /api/tenant/vendor-payments.
func (h *VendorPaymentOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authVendorPayment(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in vendorpayment.CreateVendorPaymentInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	for _, app := range in.Applications {
		if !h.vendorBillInScopeForUpdate(w, r, pool, identityID, app.VendorBillUUID) {
			return
		}
	}
	empID := resolveEmployeeID(r, identityID)
	vp, err := vendorpayment.Create(r.Context(), pool, in, empID)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to create vendor payment.")
		return
	}
	auditVendorPayment(r, pool, empID, "create", vp.ID, nil, vp)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorPayment": vp})
}

// Get handles GET /api/tenant/vendor-payments/{uuid}.
func (h *VendorPaymentOps) Get(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorPaymentByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	vp, err := vendorpayment.Get(r.Context(), pool, uuid)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to load vendor payment.")
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to load vendor payment.")
		return
	}
	info, err := vendorpayment.GetApprovalInfo(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to load vendor payment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorPayment": vp, "approval": info})
}

// Update handles PATCH /api/tenant/vendor-payments/{uuid}.
func (h *VendorPaymentOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in vendorpayment.UpdateVendorPaymentInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := vendorpayment.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := vendorpayment.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to update vendor payment.")
		return
	}
	auditVendorPayment(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorPayment": after})
}

// Delete handles DELETE /api/tenant/vendor-payments/{uuid}.
func (h *VendorPaymentOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVendorPaymentByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := vendorpayment.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := vendorpayment.SoftDelete(r.Context(), pool, id, empID); err != nil {
		vendorPaymentFail(w, err, "Failed to delete vendor payment.")
		return
	}
	auditVendorPayment(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Vendor payment deleted."})
}

// List handles GET /api/tenant/vendor-payments.
func (h *VendorPaymentOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVendorPayment(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := vendorpayment.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to list vendor payments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Search handles POST /api/tenant/vendor-payments/search.
func (h *VendorPaymentOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVendorPayment(w, r, authz.ActionRead)
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
	page, err := vendorpayment.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vendorPaymentFail(w, err, "Failed to search vendor payments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
