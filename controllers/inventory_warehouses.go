package controllers

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// InventoryWarehouseOps serves the read-only list of locations inventory can
// hold stock in. There is no warehouse master to maintain: a warehouse is a
// company_location, and locations are created, edited, made the default and
// deleted only from Configuration -> Company Info -> Locations
// (controllers/company_location.go).
//
// Routes:
//
//	GET /api/tenant/inventory/warehouses          — list
//	GET /api/tenant/inventory/warehouses/{uuid}   — get
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
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" locations.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// List GET /api/tenant/inventory/warehouses
func (h *InventoryWarehouseOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	items, err := inventory.ListWarehouses(r.Context(), pool)
	if err != nil {
		inventoryFail(w, err, "Failed to list locations.")
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
		inventoryFail(w, err, "Failed to load location.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "warehouse": wh})
}
