package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventoryadjustment"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// InventoryAdjustmentOps serves the manual stock-correction document.
//
// Routes:
//
//	GET    /api/tenant/inventory/adjustments                   — list
//	POST   /api/tenant/inventory/adjustments/search            — filter + sort
//	POST   /api/tenant/inventory/adjustments                   — create (draft)
//	GET    /api/tenant/inventory/adjustments/{uuid}            — get with lines
//	PATCH  /api/tenant/inventory/adjustments/{uuid}            — edit a draft
//	DELETE /api/tenant/inventory/adjustments/{uuid}            — soft delete
//	POST   /api/tenant/inventory/adjustments/{uuid}/transition — move status
//	POST   /api/tenant/inventory/adjustments/{uuid}/post       — apply to stock
//	GET    /api/tenant/inventory/adjustments/{uuid}/history    — status trail
type InventoryAdjustmentOps struct{}

// NewInventoryAdjustmentOps constructs the handler group.
func NewInventoryAdjustmentOps() *InventoryAdjustmentOps { return &InventoryAdjustmentOps{} }

func (h *InventoryAdjustmentOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryAdjustment, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryAdjustment), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory adjustments.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// authByUUID adds the existence guard for single-record mutations, 404ing so
// document ids cannot be enumerated.
func (h *InventoryAdjustmentOps) authByUUID(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	pool, identityID, ok := h.auth(w, r, action)
	if !ok {
		return nil, "", false
	}
	if _, err := inventoryadjustment.Get(r.Context(), pool, r.PathValue("uuid")); err != nil {
		adjustmentFail(w, err, "Failed to load adjustment.")
		return nil, "", false
	}
	return pool, identityID, true
}

// adjustmentFail maps a store error to its status code.
func adjustmentFail(w http.ResponseWriter, err error, fallback string) {
	var invalid *query.InvalidFilterError
	switch {
	case errors.Is(err, inventoryadjustment.ErrNotFound):
		fail(w, http.StatusNotFound, "Adjustment not found.")
	case errors.As(err, &invalid):
		// A bad filter key is the caller's mistake, never a 500.
		fail(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, inventoryadjustment.ErrNotEditable):
		fail(w, http.StatusConflict, "This adjustment can no longer be edited.")
	case inventoryadjustment.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, fallback)
	}
}

// List GET /api/tenant/inventory/adjustments
func (h *InventoryAdjustmentOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, req)
}

// Search POST /api/tenant/inventory/adjustments/search
func (h *InventoryAdjustmentOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
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
	h.search(w, r, pool, req)
}

func (h *InventoryAdjustmentOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, req query.Request) {
	page, err := inventoryadjustment.Search(r.Context(), pool, req)
	if err != nil {
		adjustmentFail(w, err, "Failed to search adjustments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// Get GET /api/tenant/inventory/adjustments/{uuid}
func (h *InventoryAdjustmentOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	a, err := inventoryadjustment.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		adjustmentFail(w, err, "Failed to load adjustment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "adjustment": a,
		"nextStatuses": inventoryadjustment.Machine().Next(a.StatusCode),
	})
}

// Create POST /api/tenant/inventory/adjustments
func (h *InventoryAdjustmentOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventoryadjustment.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	a, err := inventoryadjustment.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		adjustmentFail(w, err, "Failed to create adjustment.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "adjustment": a})
}

// Update PATCH /api/tenant/inventory/adjustments/{uuid}
func (h *InventoryAdjustmentOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventoryadjustment.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventoryadjustment.Update(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		adjustmentFail(w, err, "Failed to update adjustment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Adjustment updated."})
}

// Delete DELETE /api/tenant/inventory/adjustments/{uuid}
func (h *InventoryAdjustmentOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := inventoryadjustment.Delete(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		adjustmentFail(w, err, "Failed to delete adjustment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Adjustment deleted."})
}

// Transition POST /api/tenant/inventory/adjustments/{uuid}/transition
//
// Moving INTO the approved status additionally requires
// inventory_adjustment:approve. Without that second check the approval step
// would be bypassable by anyone holding plain transition rights, which is the
// entire control this document exists to provide — and it would look correct in
// review, because the handler does check a permission.
func (h *InventoryAdjustmentOps) Transition(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if body.Status == inventoryadjustment.StatusApproved {
		if !h.mayApprove(w, r, pool, identityID) {
			return
		}
	}
	a, err := inventoryadjustment.Transition(r.Context(), pool, r.PathValue("uuid"),
		body.Status, body.Note, resolveEmployeeID(r, identityID))
	if err != nil {
		adjustmentFail(w, err, "Failed to change the adjustment's status.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "adjustment": a})
}

// mayApprove checks the separate approve grant, reporting the denial itself.
func (h *InventoryAdjustmentOps) mayApprove(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInventoryAdjustment, authz.ActionApprove)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInventoryAdjustment), "action", string(authz.ActionApprove))
		fail(w, http.StatusForbidden, "You do not have permission to approve inventory adjustments.")
		return false
	}
	return true
}

// Post POST /api/tenant/inventory/adjustments/{uuid}/post — apply to stock.
func (h *InventoryAdjustmentOps) Post(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	a, err := inventoryadjustment.Post(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID))
	if err != nil {
		adjustmentFail(w, err, "Failed to post the adjustment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "adjustment": a})
}

// History GET /api/tenant/inventory/adjustments/{uuid}/history
func (h *InventoryAdjustmentOps) History(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := inventoryadjustment.History(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		adjustmentFail(w, err, "Failed to load the adjustment's history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": entries})
}
