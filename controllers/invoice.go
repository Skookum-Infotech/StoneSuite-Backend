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
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

type InvoiceOps struct{}

func NewInvoiceOps() *InvoiceOps { return &InvoiceOps{} }

func (h *InvoiceOps) authInvoice(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInvoice, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInvoice), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" invoices.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

func (h *InvoiceOps) authInvoiceByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authInvoice(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	inv, err := invoice.Get(r.Context(), pool, uuid)
	if errors.Is(err, invoice.ErrNotFound) {
		fail(w, http.StatusNotFound, "Invoice not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invoice.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, inv.OwnerUserID, "")
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceInvoice),
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Invoice not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

func invoiceFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, invoice.ErrNotFound):
		fail(w, http.StatusNotFound, "Invoice not found.")
	case errors.Is(err, invoice.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	default:
		var ce invoice.ClientError
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

func (h *InvoiceOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authInvoice(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in invoice.CreateInvoiceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	inv, err := invoice.Create(r.Context(), pool, in, empID)
	if err != nil {
		invoiceFail(w, err, "Failed to create invoice.")
		return
	}
	auditInvoice(r, pool, empID, "create", inv.ID, nil, inv)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "invoice": inv})
}

func (h *InvoiceOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authInvoiceByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	inv, err := invoice.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		invoiceFail(w, err, "Failed to load invoice.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invoice": inv})
}

func (h *InvoiceOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in invoice.UpdateInvoiceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := invoice.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := invoice.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		invoiceFail(w, err, "Failed to update invoice.")
		return
	}
	auditInvoice(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invoice": after})
}

func (h *InvoiceOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := invoice.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := invoice.SoftDelete(r.Context(), pool, id, empID); err != nil {
		invoiceFail(w, err, "Failed to delete invoice.")
		return
	}
	auditInvoice(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Invoice deleted."})
}

func (h *InvoiceOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authInvoice(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := invoice.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		invoiceFail(w, err, "Failed to list invoices.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

func (h *InvoiceOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authInvoice(w, r, authz.ActionRead)
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
	page, err := invoice.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		invoiceFail(w, err, "Failed to search invoices.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
