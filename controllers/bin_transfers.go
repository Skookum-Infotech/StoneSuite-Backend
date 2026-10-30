// controllers/bin_transfers.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/bintransfer"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// BinTransferOps serves bin transfers: documents that move slabs between bins
// inside one location, optionally approved by the chain configured under
// Configuration > Workflows > Bin Transfer, and applied by Complete.
//
// Routes:
//
//	GET    /api/tenant/inventory/bin-transfers                   — list
//	POST   /api/tenant/inventory/bin-transfers/search            — filter + sort
//	POST   /api/tenant/inventory/bin-transfers                   — create (draft)
//	GET    /api/tenant/inventory/bin-transfers/{uuid}            — get with lines + approval
//	PATCH  /api/tenant/inventory/bin-transfers/{uuid}            — edit a draft
//	DELETE /api/tenant/inventory/bin-transfers/{uuid}            — soft delete a draft/cancelled one
//	POST   /api/tenant/inventory/bin-transfers/{uuid}/transition — submit / revise / cancel
//	POST   /api/tenant/inventory/bin-transfers/{uuid}/approve    — approver sign-off
//	POST   /api/tenant/inventory/bin-transfers/{uuid}/reject     — approver veto
//	POST   /api/tenant/inventory/bin-transfers/{uuid}/complete   — apply the moves
//	GET    /api/tenant/inventory/bin-transfers/{uuid}/history    — status trail
type BinTransferOps struct{}

// NewBinTransferOps constructs the handler group.
func NewBinTransferOps() *BinTransferOps { return &BinTransferOps{} }

// authBinTransfer resolves JWT + tenant pool + the bin_transfer:<action> grant
// for requests with no specific record yet (list/search/create).
func (h *BinTransferOps) authBinTransfer(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceBinTransfer, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceBinTransfer), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" bin transfers.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authBinTransferByUUID adds the row-level IDOR guard for a single record. Denial
// is a 404, not a 403, so ids cannot be enumerated outside the caller's scope.
func (h *BinTransferOps) authBinTransferByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authBinTransfer(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	bt, err := bintransfer.Get(r.Context(), pool, uuid)
	if errors.Is(err, bintransfer.ErrNotFound) {
		fail(w, http.StatusNotFound, "Bin transfer not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load bin transfer.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, bt.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceBinTransfer),
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Bin transfer not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

// List GET /api/tenant/inventory/bin-transfers
func (h *BinTransferOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authBinTransfer(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	h.writePage(w, r, pool, scope, identityID, req, "Failed to list bin transfers.")
}

// Search POST /api/tenant/inventory/bin-transfers/search
func (h *BinTransferOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authBinTransfer(w, r, authz.ActionRead)
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
	h.writePage(w, r, pool, scope, identityID, req, "Failed to search bin transfers.")
}

func (h *BinTransferOps) writePage(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, scope authz.Scope, identityID string, req query.Request, failMsg string) {
	page, err := bintransfer.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		binTransferFail(w, err, failMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Create POST /api/tenant/inventory/bin-transfers
func (h *BinTransferOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authBinTransfer(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in bintransfer.Input
	raw, ok := readJSONBody(w, r, &in)
	if !ok {
		return
	}
	key, ok := idempotencyKeyFromRequest(w, r)
	if !ok {
		return
	}
	if key != "" {
		in.IdempotencyKey = callerIdempotencyKey(identityID, key)
		in.RequestHash = idempotency.HashRequest(raw)
	}
	bt, err := bintransfer.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		binTransferFail(w, err, "Failed to create bin transfer.")
		return
	}
	auditBT(r, pool, identityID, "create", bt.ID, nil, bt)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "binTransfer": bt})
}

// Get GET /api/tenant/inventory/bin-transfers/{uuid}
func (h *BinTransferOps) Get(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authBinTransferByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	bt, err := bintransfer.Get(r.Context(), pool, uuid)
	if err != nil {
		binTransferFail(w, err, "Failed to load bin transfer.")
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		binTransferFail(w, err, "Failed to load bin transfer.")
		return
	}
	info, err := bintransfer.GetApprovalInfo(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		binTransferFail(w, err, "Failed to load bin transfer.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "binTransfer": bt, "approval": info})
}

// Update PATCH /api/tenant/inventory/bin-transfers/{uuid}
func (h *BinTransferOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authBinTransferByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in bintransfer.Input
	if _, ok := readJSONBody(w, r, &in); !ok {
		return
	}
	before, _ := bintransfer.Get(r.Context(), pool, uuid)
	after, err := bintransfer.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		binTransferFail(w, err, "Failed to update bin transfer.")
		return
	}
	auditBT(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "binTransfer": after})
}

// Delete DELETE /api/tenant/inventory/bin-transfers/{uuid}
func (h *BinTransferOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authBinTransferByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := bintransfer.Get(r.Context(), pool, uuid)
	if err := bintransfer.Delete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		binTransferFail(w, err, "Failed to delete bin transfer.")
		return
	}
	auditBTDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Bin transfer deleted."})
}
