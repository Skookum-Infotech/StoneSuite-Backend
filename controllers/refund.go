package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/creditmemo"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/payment"
	"stonesuite-backend/query"
	"stonesuite-backend/refund"
	"stonesuite-backend/tenancy"
)

type RefundOps struct{}

func NewRefundOps() *RefundOps { return &RefundOps{} }

func (h *RefundOps) authRefund(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceRefund, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceRefund), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" refunds.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

func (h *RefundOps) authRefundByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authRefund(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	rf, err := refund.Get(r.Context(), pool, uuid)
	if errors.Is(err, refund.ErrNotFound) {
		fail(w, http.StatusNotFound, "Refund not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load refund.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, rf.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceRefund),
			"action", string(action), "scope", string(scope))
		// 404, not 403: a 403 would confirm the id exists and let it be enumerated.
		fail(w, http.StatusNotFound, "Refund not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

// paymentInScopeForUpdate checks the caller holds payment:update and that the
// target payment is within their scope, writing the response and returning
// false on denial (404 on scope denial, per the IDOR convention). Used by
// Apply/Unapply because those endpoints mutate a payment's refunded-total
// rollup as a side effect of a refund-side action.
func (h *RefundOps) paymentInScopeForUpdate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, paymentUUID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourcePayment, authz.ActionUpdate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourcePayment), "action", string(authz.ActionUpdate))
		fail(w, http.StatusForbidden, "You do not have permission to update payments.")
		return false
	}
	if decision.Scope == authz.ScopeAll {
		return true
	}
	p, err := payment.Get(r.Context(), pool, paymentUUID)
	if errors.Is(err, payment.ErrNotFound) {
		fail(w, http.StatusNotFound, "Payment not found.")
		return false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load payment.")
		return false
	}
	allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, identityID, p.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", paymentUUID, "resource", string(authz.ResourcePayment),
			"action", "update", "scope", string(decision.Scope))
		fail(w, http.StatusNotFound, "Payment not found.")
		return false
	}
	return true
}

// creditMemoInScopeForUpdate mirrors paymentInScopeForUpdate for the
// credit-memo source.
func (h *RefundOps) creditMemoInScopeForUpdate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, creditMemoUUID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceCreditMemo, authz.ActionUpdate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceCreditMemo), "action", string(authz.ActionUpdate))
		fail(w, http.StatusForbidden, "You do not have permission to update credit memos.")
		return false
	}
	if decision.Scope == authz.ScopeAll {
		return true
	}
	cm, err := creditmemo.Get(r.Context(), pool, creditMemoUUID)
	if errors.Is(err, creditmemo.ErrNotFound) {
		fail(w, http.StatusNotFound, "Credit memo not found.")
		return false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load credit memo.")
		return false
	}
	allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, identityID, cm.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", creditMemoUUID, "resource", string(authz.ResourceCreditMemo),
			"action", "update", "scope", string(decision.Scope))
		fail(w, http.StatusNotFound, "Credit memo not found.")
		return false
	}
	return true
}

func refundFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, refund.ErrNotFound):
		fail(w, http.StatusNotFound, "Refund not found.")
	case errors.Is(err, refund.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	default:
		var ce refund.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		// Apply/Unapply reach into payment/credit_memo for the source-row lock,
		// so a bad source surfaces as one of those packages' ClientError. Without
		// these arms it would fall through to 500.
		var pce payment.ClientError
		if errors.As(err, &pce) {
			fail(w, http.StatusBadRequest, pce.Error())
			return
		}
		var cce creditmemo.ClientError
		if errors.As(err, &cce) {
			fail(w, http.StatusBadRequest, cce.Error())
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

func (h *RefundOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authRefund(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in refund.CreateRefundInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	rf, err := refund.Create(r.Context(), pool, in, empID)
	if err != nil {
		refundFail(w, err, "Failed to create refund.")
		return
	}
	auditRefund(r, pool, empID, "create", rf.ID, nil, rf)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "refund": rf})
}

func (h *RefundOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authRefundByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	rf, err := refund.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		refundFail(w, err, "Failed to load refund.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "refund": rf})
}

func (h *RefundOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authRefundByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in refund.UpdateRefundInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := refund.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := refund.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		refundFail(w, err, "Failed to update refund.")
		return
	}
	auditRefund(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "refund": after})
}

func (h *RefundOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authRefundByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := refund.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := refund.SoftDelete(r.Context(), pool, id, empID); err != nil {
		refundFail(w, err, "Failed to delete refund.")
		return
	}
	auditRefund(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Refund deleted."})
}

func (h *RefundOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authRefund(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := refund.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		refundFail(w, err, "Failed to list refunds.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

func (h *RefundOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authRefund(w, r, authz.ActionRead)
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
	page, err := refund.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		refundFail(w, err, "Failed to search refunds.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
