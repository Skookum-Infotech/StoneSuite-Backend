package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventorytransfer"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// InventoryTransferOps serves warehouse-to-warehouse stock movement.
//
// Two-legged: ship deducts at the source, receive adds at the destination.
// Between the two the stock is in neither warehouse, which is why in-transit
// gets its own read.
//
// Routes:
//
//	GET    /api/tenant/inventory/transfers                   — list
//	POST   /api/tenant/inventory/transfers/search            — filter + sort
//	GET    /api/tenant/inventory/transfers/in-transit        — shipped, not landed
//	POST   /api/tenant/inventory/transfers                   — create (draft)
//	GET    /api/tenant/inventory/transfers/{uuid}            — get with lines
//	PATCH  /api/tenant/inventory/transfers/{uuid}            — edit a draft
//	DELETE /api/tenant/inventory/transfers/{uuid}            — soft delete
//	POST   /api/tenant/inventory/transfers/{uuid}/transition — move status
//	POST   /api/tenant/inventory/transfers/{uuid}/ship       — leave the source
//	POST   /api/tenant/inventory/transfers/{uuid}/receive    — land at the destination
//	GET    /api/tenant/inventory/transfers/{uuid}/history    — status trail
type InventoryTransferOps struct{}

// NewInventoryTransferOps constructs the handler group.
func NewInventoryTransferOps() *InventoryTransferOps { return &InventoryTransferOps{} }

func (h *InventoryTransferOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryTransfer, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryTransfer), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory transfers.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// authByUUID adds the existence guard for single-record mutations, 404ing so
// document ids cannot be enumerated.
func (h *InventoryTransferOps) authByUUID(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	pool, identityID, ok := h.auth(w, r, action)
	if !ok {
		return nil, "", false
	}
	if _, err := inventorytransfer.Get(r.Context(), pool, r.PathValue("uuid")); err != nil {
		transferFail(w, err, "Failed to load transfer.")
		return nil, "", false
	}
	return pool, identityID, true
}

func transferFail(w http.ResponseWriter, err error, fallback string) {
	var invalid *query.InvalidFilterError
	switch {
	case errors.Is(err, inventorytransfer.ErrNotFound):
		fail(w, http.StatusNotFound, "Transfer not found.")
	case errors.As(err, &invalid):
		fail(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, inventorytransfer.ErrNotEditable):
		fail(w, http.StatusConflict, "This transfer can no longer be edited.")
	case inventorytransfer.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, fallback)
	}
}

// List GET /api/tenant/inventory/transfers
func (h *InventoryTransferOps) List(w http.ResponseWriter, r *http.Request) {
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

// Search POST /api/tenant/inventory/transfers/search
func (h *InventoryTransferOps) Search(w http.ResponseWriter, r *http.Request) {
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

func (h *InventoryTransferOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, req query.Request) {
	page, err := inventorytransfer.Search(r.Context(), pool, req)
	if err != nil {
		transferFail(w, err, "Failed to search transfers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// InTransit GET /api/tenant/inventory/transfers/in-transit — stock that has
// left one warehouse and not reached the other, and so is counted in neither.
// This read is how that quantity is recovered without a phantom warehouse row.
func (h *InventoryTransferOps) InTransit(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	records, err := inventorytransfer.InTransit(r.Context(), pool)
	if err != nil {
		transferFail(w, err, "Failed to load in-transit transfers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": records})
}

// Get GET /api/tenant/inventory/transfers/{uuid}
func (h *InventoryTransferOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	t, err := inventorytransfer.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		transferFail(w, err, "Failed to load transfer.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "transfer": t,
		"nextStatuses": inventorytransfer.Machine().Next(t.StatusCode),
	})
}

// Create POST /api/tenant/inventory/transfers
func (h *InventoryTransferOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventorytransfer.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	t, err := inventorytransfer.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		transferFail(w, err, "Failed to create transfer.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "transfer": t})
}

// Update PATCH /api/tenant/inventory/transfers/{uuid}
func (h *InventoryTransferOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventorytransfer.Input
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventorytransfer.Update(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		transferFail(w, err, "Failed to update transfer.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Transfer updated."})
}

// Delete DELETE /api/tenant/inventory/transfers/{uuid}
func (h *InventoryTransferOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := inventorytransfer.Delete(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		transferFail(w, err, "Failed to delete transfer.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Transfer deleted."})
}

// Transition POST /api/tenant/inventory/transfers/{uuid}/transition
//
// Moving into the approved status additionally requires
// inventory_transfer:approve — without that second check, plain transition
// rights would be enough to sign off your own transfer and the approval step
// would be decoration.
func (h *InventoryTransferOps) Transition(w http.ResponseWriter, r *http.Request) {
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
	if body.Status == inventorytransfer.StatusApproved && !h.mayApprove(w, r, pool, identityID) {
		return
	}
	t, err := inventorytransfer.Transition(r.Context(), pool, r.PathValue("uuid"),
		body.Status, body.Note, resolveEmployeeID(r, identityID))
	if err != nil {
		transferFail(w, err, "Failed to change the transfer's status.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "transfer": t})
}

// mayApprove checks the separate approve grant, reporting the denial itself.
func (h *InventoryTransferOps) mayApprove(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInventoryTransfer, authz.ActionApprove)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInventoryTransfer), "action", string(authz.ActionApprove))
		fail(w, http.StatusForbidden, "You do not have permission to approve inventory transfers.")
		return false
	}
	return true
}

// Ship POST /api/tenant/inventory/transfers/{uuid}/ship — stock leaves the source.
func (h *InventoryTransferOps) Ship(w http.ResponseWriter, r *http.Request) {
	h.leg(w, r, inventorytransfer.Ship, "Failed to ship the transfer.")
}

// Receive POST /api/tenant/inventory/transfers/{uuid}/receive — stock lands.
func (h *InventoryTransferOps) Receive(w http.ResponseWriter, r *http.Request) {
	h.leg(w, r, inventorytransfer.Receive, "Failed to receive the transfer.")
}

// leg runs the shared dispatch for the two stock-moving endpoints.
func (h *InventoryTransferOps) leg(w http.ResponseWriter, r *http.Request,
	op func(ctx context.Context, pool *pgxpool.Pool, uuid string, actor int) (*inventorytransfer.Transfer, error),
	failMsg string) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionTransition)
	if !ok {
		return
	}
	t, err := op(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID))
	if err != nil {
		transferFail(w, err, failMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "transfer": t})
}

// History GET /api/tenant/inventory/transfers/{uuid}/history
func (h *InventoryTransferOps) History(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := inventorytransfer.History(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		transferFail(w, err, "Failed to load the transfer's history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": entries})
}
