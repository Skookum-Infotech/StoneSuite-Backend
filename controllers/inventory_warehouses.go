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

// InventoryWarehouseOps serves warehouse master data. lkp_warehouse has existed
// since the sales-order migration with its own uuid, but no route ever served
// it.
//
// Routes:
//
//	GET    /api/tenant/inventory/warehouses                     — list
//	POST   /api/tenant/inventory/warehouses                     — create
//	GET    /api/tenant/inventory/warehouses/{uuid}              — get
//	PATCH  /api/tenant/inventory/warehouses/{uuid}              — update
//	DELETE /api/tenant/inventory/warehouses/{uuid}              — soft delete
//	POST   /api/tenant/inventory/warehouses/{uuid}/set-default  — move the default
type InventoryWarehouseOps struct{}

// NewInventoryWarehouseOps constructs the handler group.
func NewInventoryWarehouseOps() *InventoryWarehouseOps { return &InventoryWarehouseOps{} }

func (h *InventoryWarehouseOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceWarehouse, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceWarehouse), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" warehouses.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// authByUUID adds the existence guard for single-record routes. Warehouses are
// tenant-global with no owner, so this is an existence check rather than an
// ownership check — but it still 404s, because the reason (not letting ids be
// enumerated) is the same.
func (h *InventoryWarehouseOps) authByUUID(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	pool, identityID, ok := h.auth(w, r, action)
	if !ok {
		return nil, "", false
	}
	if _, err := inventory.GetWarehouse(r.Context(), pool, r.PathValue("uuid")); err != nil {
		inventoryFail(w, err, "Failed to load warehouse.")
		return nil, "", false
	}
	return pool, identityID, true
}

// List GET /api/tenant/inventory/warehouses
func (h *InventoryWarehouseOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	items, err := inventory.ListWarehouses(r.Context(), pool, r.URL.Query().Get("includeInactive") == "true")
	if err != nil {
		inventoryFail(w, err, "Failed to list warehouses.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": items})
}

// Get GET /api/tenant/inventory/warehouses/{uuid}
func (h *InventoryWarehouseOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	wh, err := inventory.GetWarehouse(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load warehouse.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "warehouse": wh})
}

// Create POST /api/tenant/inventory/warehouses
func (h *InventoryWarehouseOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventory.WarehouseInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	wh, err := inventory.CreateWarehouse(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, "Failed to create warehouse.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "warehouse": wh})
}

// Update PATCH /api/tenant/inventory/warehouses/{uuid}
func (h *InventoryWarehouseOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.WarehouseInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventory.UpdateWarehouse(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to update warehouse.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Warehouse updated."})
}

// SetDefault POST /api/tenant/inventory/warehouses/{uuid}/set-default
func (h *InventoryWarehouseOps) SetDefault(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	if err := inventory.SetDefaultWarehouse(r.Context(), pool, r.PathValue("uuid")); err != nil {
		inventoryFail(w, err, "Failed to set the default warehouse.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Default warehouse updated."})
}

// Delete DELETE /api/tenant/inventory/warehouses/{uuid}
func (h *InventoryWarehouseOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := inventory.DeleteWarehouse(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to delete warehouse.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Warehouse deleted."})
}
