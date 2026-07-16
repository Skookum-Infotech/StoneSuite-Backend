package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/creditmemo"
	"stonesuite-backend/invoice"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

type CreditMemoOps struct{}

func NewCreditMemoOps() *CreditMemoOps { return &CreditMemoOps{} }

func (h *CreditMemoOps) authCreditMemo(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceCreditMemo, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceCreditMemo), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" credit memos.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

func (h *CreditMemoOps) authCreditMemoByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authCreditMemo(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	cm, err := creditmemo.Get(r.Context(), pool, uuid)
	if errors.Is(err, creditmemo.ErrNotFound) {
		fail(w, http.StatusNotFound, "Credit memo not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load credit memo.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, cm.OwnerUserID, "")
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceCreditMemo),
			"action", string(action), "scope", string(scope))
		// 404, not 403: a 403 would confirm the id exists and let it be enumerated.
		fail(w, http.StatusNotFound, "Credit memo not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

// invoiceInScopeForUpdate checks the caller holds invoice:update and that the
// target invoice is within their scope, writing the response and returning
// false on denial (404 on scope denial, per the IDOR convention). Used by
// Apply/Unapply because those endpoints mutate an invoice's AR balance as a
// side effect of a credit-memo-side action.
func (h *CreditMemoOps) invoiceInScopeForUpdate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, invoiceUUID string) bool {
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

func creditMemoFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, creditmemo.ErrNotFound):
		fail(w, http.StatusNotFound, "Credit memo not found.")
	case errors.Is(err, creditmemo.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	default:
		var ce creditmemo.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		// Apply/Unapply reach into the invoice package for the row lock and the
		// AR rollup, so a bad invoice surfaces as invoice.ClientError.
		var ice invoice.ClientError
		if errors.As(err, &ice) {
			fail(w, http.StatusBadRequest, ice.Error())
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

func (h *CreditMemoOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authCreditMemo(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in creditmemo.CreateCreditMemoInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	// A new memo starts at DRFT and cannot move credit (spec AD-7), so Create
	// never applies. Reject rather than silently ignoring the field.
	if len(in.Applications) > 0 {
		fail(w, http.StatusBadRequest, "A new credit memo starts as a draft and cannot be applied; approve it first, then apply.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	cm, err := creditmemo.Create(r.Context(), pool, in, empID)
	if err != nil {
		creditMemoFail(w, err, "Failed to create credit memo.")
		return
	}
	auditCreditMemo(r, pool, empID, "create", cm.ID, nil, cm)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "creditMemo": cm})
}

func (h *CreditMemoOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authCreditMemoByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	cm, err := creditmemo.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		creditMemoFail(w, err, "Failed to load credit memo.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "creditMemo": cm})
}

func (h *CreditMemoOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCreditMemoByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in creditmemo.UpdateCreditMemoInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := creditmemo.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := creditmemo.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		creditMemoFail(w, err, "Failed to update credit memo.")
		return
	}
	auditCreditMemo(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "creditMemo": after})
}

func (h *CreditMemoOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCreditMemoByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := creditmemo.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := creditmemo.SoftDelete(r.Context(), pool, id, empID); err != nil {
		creditMemoFail(w, err, "Failed to delete credit memo.")
		return
	}
	auditCreditMemo(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Credit memo deleted."})
}

func (h *CreditMemoOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authCreditMemo(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := creditmemo.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		creditMemoFail(w, err, "Failed to list credit memos.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

func (h *CreditMemoOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authCreditMemo(w, r, authz.ActionRead)
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
	page, err := creditmemo.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		creditMemoFail(w, err, "Failed to search credit memos.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
