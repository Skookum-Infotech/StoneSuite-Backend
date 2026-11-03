package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
)

// Stock on hand, per item and location. Same handler group and the same
// inventory_item grant as the catalogue: stock is catalogue-adjacent reference
// data, so a role that can read items can read how much of each is held, and one
// that can update items can set a reorder point.
//
//	GET   /api/tenant/inventory/items/{uuid}/stock   — one item's stock per location
//	POST  /api/tenant/inventory/stock/totals         — summed stock for a page of items
//	PATCH /api/tenant/inventory/stock/reorder-point  — set an item's reorder point at a location
//
// Stock itself is never written here: it moves only when a document posts.

// maxStockTotalItems bounds one totals request; the Items list asks for a page
// (25 rows) at a time.
const maxStockTotalItems = 200

// StockForItem GET /api/tenant/inventory/items/{uuid}/stock
func (h *InventoryOps) StockForItem(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authInventory(w, r, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := inventory.StockForItem(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load stock.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": rows})
}

// StockTotals POST /api/tenant/inventory/stock/totals
func (h *InventoryOps) StockTotals(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authInventory(w, r, authz.ActionRead)
	if !ok {
		return
	}
	var body struct {
		ItemIDs []string `json:"itemIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if len(body.ItemIDs) > maxStockTotalItems {
		fail(w, http.StatusBadRequest, "Too many items in one request.")
		return
	}
	totals, err := inventory.StockTotals(r.Context(), pool, body.ItemIDs)
	if err != nil {
		inventoryFail(w, err, "Failed to load stock totals.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals})
}

// SetReorderPoint PATCH /api/tenant/inventory/stock/reorder-point
func (h *InventoryOps) SetReorderPoint(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authInventory(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var body struct {
		ItemID       string  `json:"itemId"`
		WarehouseID  int     `json:"warehouseId"`
		ReorderPoint float64 `json:"reorderPoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "itemId, warehouseId and reorderPoint are required, and reorderPoint must be a number.")
		return
	}
	row, err := inventory.SetReorderPoint(r.Context(), pool, body.ItemID, body.WarehouseID, body.ReorderPoint)
	if err != nil {
		inventoryFail(w, err, "Failed to save the reorder point.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": row})
}
