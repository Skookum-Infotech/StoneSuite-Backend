package controllers

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// InventoryBinOps serves bin/location master data — yards, racks, A-frames,
// aisles, shelves and staging areas inside a warehouse.
//
// Bins locate serialized units only; inventory_stock stays keyed on
// (item, warehouse), so moving a unit between bins is stock-neutral and writes
// no ledger row.
//
// Routes:
//
//	GET    /api/tenant/inventory/bins              — list (?warehouseId=)
//	GET    /api/tenant/inventory/bins/tree         — nested by parent
//	POST   /api/tenant/inventory/bins              — create
//	GET    /api/tenant/inventory/bins/{uuid}       — get
//	PATCH  /api/tenant/inventory/bins/{uuid}       — update (incl. reparent)
//	DELETE /api/tenant/inventory/bins/{uuid}       — soft delete
type InventoryBinOps struct{}

// NewInventoryBinOps constructs the handler group.
func NewInventoryBinOps() *InventoryBinOps { return &InventoryBinOps{} }

func (h *InventoryBinOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryBin, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryBin), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory bins.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// authByUUID adds the existence guard for single-record MUTATIONS, 404ing
// rather than 403ing so bin ids cannot be enumerated.
//
// Read handlers deliberately do not call it: GetBin already returns ErrNotFound
// for a missing or malformed uuid and inventoryFail maps that to 404, so the
// guard would fetch the same row twice for no gain. Bins are tenant-global with
// no owner column, so there is no ownership state to leak on a read.
func (h *InventoryBinOps) authByUUID(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	pool, identityID, ok := h.auth(w, r, action)
	if !ok {
		return nil, "", false
	}
	if _, err := inventory.GetBin(r.Context(), pool, r.PathValue("uuid")); err != nil {
		inventoryFail(w, err, "Failed to load bin.")
		return nil, "", false
	}
	return pool, identityID, true
}

// List GET /api/tenant/inventory/bins
func (h *InventoryBinOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	bins, err := inventory.ListBins(r.Context(), pool,
		r.URL.Query().Get("warehouseId"), r.URL.Query().Get("includeInactive") == "true")
	if err != nil {
		inventoryFail(w, err, "Failed to list bins.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": bins})
}

// Tree GET /api/tenant/inventory/bins/tree
func (h *InventoryBinOps) Tree(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	bins, err := inventory.BinTree(r.Context(), pool,
		r.URL.Query().Get("warehouseId"), r.URL.Query().Get("includeInactive") == "true")
	if err != nil {
		inventoryFail(w, err, "Failed to load the bin tree.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": bins})
}

// Get GET /api/tenant/inventory/bins/{uuid}
func (h *InventoryBinOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	bin, err := inventory.GetBin(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load bin.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "bin": bin})
}

// Create POST /api/tenant/inventory/bins
func (h *InventoryBinOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventory.BinInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	bin, err := inventory.CreateBin(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, "Failed to create bin.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "bin": bin})
}

// Update PATCH /api/tenant/inventory/bins/{uuid}
func (h *InventoryBinOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.BinInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventory.UpdateBin(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to update bin.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Bin updated."})
}

// Delete DELETE /api/tenant/inventory/bins/{uuid}
func (h *InventoryBinOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := inventory.DeleteBin(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to delete bin.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Bin deleted."})
}
