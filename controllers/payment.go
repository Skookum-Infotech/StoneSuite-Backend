package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/invoice"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/payment"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

type PaymentOps struct{}

func NewPaymentOps() *PaymentOps { return &PaymentOps{} }

func (h *PaymentOps) authPayment(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourcePayment, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourcePayment), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" payments.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

func (h *PaymentOps) authPaymentByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authPayment(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	p, err := payment.Get(r.Context(), pool, uuid)
	if errors.Is(err, payment.ErrNotFound) {
		fail(w, http.StatusNotFound, "Payment not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load payment.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, p.OwnerUserID, "")
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourcePayment),
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Payment not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

// invoiceInScopeForUpdate checks the caller holds invoice:update and that the
// target invoice is within their scope, writing the response and returning
// false on denial (404 on scope denial, per the IDOR convention). Used by
// Apply/Unapply because those endpoints mutate an invoice's AR balance as a
// side effect of a payment-side action.
func (h *PaymentOps) invoiceInScopeForUpdate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, invoiceUUID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInvoice, authz.ActionUpdate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInvoice), "action", string(authz.ActionUpdate))
		fail(w, http.StatusForbidden, "You do not have permission to update invoices.")
		return false
	}
	if decision.Scope == authz.ScopeAll {
		return true
	}
	inv, err := invoice.Get(r.Context(), pool, invoiceUUID)
	if errors.Is(err, invoice.ErrNotFound) {
		fail(w, http.StatusNotFound, "Invoice not found.")
		return false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invoice.")
		return false
	}
	allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, identityID, inv.OwnerUserID, "")
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", invoiceUUID, "resource", string(authz.ResourceInvoice),
			"action", "update", "scope", string(decision.Scope))
		fail(w, http.StatusNotFound, "Invoice not found.")
		return false
	}
	return true
}

func paymentFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, payment.ErrNotFound):
		fail(w, http.StatusNotFound, "Payment not found.")
	case errors.Is(err, payment.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	default:
		var ce payment.ClientError
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

func (h *PaymentOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authPayment(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in payment.CreatePaymentInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	p, err := payment.Create(r.Context(), pool, in, empID)
	if err != nil {
		paymentFail(w, err, "Failed to create payment.")
		return
	}
	auditPayment(r, pool, empID, "create", p.ID, nil, p)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "payment": p})
}

func (h *PaymentOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authPaymentByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	p, err := payment.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		paymentFail(w, err, "Failed to load payment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}

func (h *PaymentOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in payment.UpdatePaymentInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := payment.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := payment.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		paymentFail(w, err, "Failed to update payment.")
		return
	}
	auditPayment(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": after})
}

func (h *PaymentOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := payment.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := payment.SoftDelete(r.Context(), pool, id, empID); err != nil {
		paymentFail(w, err, "Failed to delete payment.")
		return
	}
	auditPayment(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Payment deleted."})
}

func (h *PaymentOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authPayment(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := payment.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		paymentFail(w, err, "Failed to list payments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

func (h *PaymentOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authPayment(w, r, authz.ActionRead)
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
	page, err := payment.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		paymentFail(w, err, "Failed to search payments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
