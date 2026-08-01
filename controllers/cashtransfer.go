package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

type CashTransferOps struct{}

func NewCashTransferOps() *CashTransferOps { return &CashTransferOps{} }

func (h *CashTransferOps) authCashTransfer(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceCashTransfer, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceCashTransfer), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" cash transfers.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

func (h *CashTransferOps) authCashTransferByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authCashTransfer(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	ct, err := cashtransfer.Get(r.Context(), pool, uuid)
	if errors.Is(err, cashtransfer.ErrNotFound) {
		fail(w, http.StatusNotFound, "Cash transfer not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load cash transfer.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, ct.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceCashTransfer),
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Cash transfer not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

func ctFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, cashtransfer.ErrNotFound):
		fail(w, http.StatusNotFound, "Cash transfer not found.")
	case errors.Is(err, cashtransfer.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, cashtransfer.ErrAlreadyPosted):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, cashtransfer.ErrNotPosted):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, cashtransfer.ErrPeriodClosed):
		fail(w, http.StatusConflict,
			"The accounting period for this date is closed. Reopen it to post.")
	case errors.Is(err, cashtransfer.ErrNoAccountingPeriod):
		fail(w, http.StatusConflict,
			"No accounting period covers this date. Generate the fiscal year first.")
	case errors.Is(err, cashtransfer.ErrVersionMismatch):
		fail(w, http.StatusConflict, "This cash transfer was changed by someone else. Reload and try again.")
	default:
		var ce cashtransfer.ClientError
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

func (h *CashTransferOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authCashTransfer(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in cashtransfer.CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	ct, err := cashtransfer.Create(r.Context(), pool, in, empID)
	if err != nil {
		ctFail(w, err, "Failed to create cash transfer.")
		return
	}
	auditCT(r, pool, empID, "create", ct.ID, nil, ct)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "cashTransfer": ct})
}

func (h *CashTransferOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authCashTransferByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	ct, err := cashtransfer.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		ctFail(w, err, "Failed to load cash transfer.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": ct})
}

func (h *CashTransferOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in cashtransfer.UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := cashtransfer.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := cashtransfer.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		ctFail(w, err, "Failed to update cash transfer.")
		return
	}
	auditCT(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": after})
}

func (h *CashTransferOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := cashtransfer.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := cashtransfer.SoftDelete(r.Context(), pool, id, empID); err != nil {
		ctFail(w, err, "Failed to delete cash transfer.")
		return
	}
	auditCT(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Cash transfer deleted."})
}

func (h *CashTransferOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authCashTransfer(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := cashtransfer.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		ctFail(w, err, "Failed to list cash transfers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

func (h *CashTransferOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authCashTransfer(w, r, authz.ActionRead)
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
	page, err := cashtransfer.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		ctFail(w, err, "Failed to search cash transfers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
