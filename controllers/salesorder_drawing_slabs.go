package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/storage"
	"stonesuite-backend/tenancy"
)

// SalesOrderDrawingSlabOps handles the slabs a sales order's Material drawing
// lays pieces on. Adding a slab to the drawing reserves it for the order.
//
// Routes:
//
//	GET    /api/tenant/sales-orders/{uuid}/drawing/slab-options — the order's slab items with their usable slabs
//	POST   /api/tenant/sales-orders/{uuid}/drawing/slabs        — hold one slab for the order
//	DELETE /api/tenant/sales-orders/{uuid}/drawing/slabs/{slabId} — release it
//
// RBAC follows the order, like the drawing itself: sales_order:read to list,
// sales_order:update to hold or release. A salesperson needs no inventory rights
// to pick a slab, the same way the order's Inventory tab needs none.
type SalesOrderDrawingSlabOps struct {
	orders *SalesOrderOps
	r2     *storage.Client // nil when R2 is not configured: photos are simply omitted
}

// NewSalesOrderDrawingSlabOps constructs the handler group.
func NewSalesOrderDrawingSlabOps(orders *SalesOrderOps, r2 *storage.Client) *SalesOrderDrawingSlabOps {
	return &SalesOrderDrawingSlabOps{orders: orders, r2: r2}
}

// drawingSlabUnit is a unit plus a short-lived URL for its photo.
type drawingSlabUnit struct {
	inventory.Unit
	PhotoURL string `json:"photoUrl,omitempty"`
}

type drawingSlabGroup struct {
	ItemID   string            `json:"itemId"`
	ItemName string            `json:"itemName"`
	ItemSKU  string            `json:"itemSku"`
	Units    []drawingSlabUnit `json:"units"`
}

// Options GET /api/tenant/sales-orders/{uuid}/drawing/slab-options
func (h *SalesOrderDrawingSlabOps) Options(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.orders.authSOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	groups, err := inventory.ListUnitsForSalesOrder(r.Context(), pool, uuid)
	if err != nil {
		inventoryFail(w, err, "Failed to load slabs for this order.")
		return
	}
	r2 := h.r2.WithBucket(tenant.R2Bucket)
	out := make([]drawingSlabGroup, 0, len(groups))
	for _, g := range groups {
		units := make([]drawingSlabUnit, 0, len(g.Units))
		for _, u := range g.Units {
			units = append(units, drawingSlabUnit{Unit: u, PhotoURL: presignUnitPhoto(r, r2, u.PhotoKey)})
		}
		out = append(out, drawingSlabGroup{ItemID: g.ItemID, ItemName: g.ItemName, ItemSKU: g.ItemSKU, Units: units})
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "items": out})
}

// Hold POST /api/tenant/sales-orders/{uuid}/drawing/slabs
func (h *SalesOrderDrawingSlabOps) Hold(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.orders.authSOByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var body struct {
		SlabID string `json:"slabId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SlabID == "" {
		fail(w, http.StatusBadRequest, "Choose a slab to add.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	if err := inventory.HoldUnitForSalesOrder(r.Context(), pool, uuid, body.SlabID, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to reserve the slab.")
		return
	}
	u, err := inventory.GetUnit(r.Context(), pool, body.SlabID)
	if err != nil {
		inventoryFail(w, err, "Failed to load the slab.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"unit":    drawingSlabUnit{Unit: *u, PhotoURL: presignUnitPhoto(r, h.r2.WithBucket(tenant.R2Bucket), u.PhotoKey)},
	})
}

// Release DELETE /api/tenant/sales-orders/{uuid}/drawing/slabs/{slabId}
func (h *SalesOrderDrawingSlabOps) Release(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.orders.authSOByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	if err := inventory.ReleaseUnitFromSalesOrder(r.Context(), pool, uuid, r.PathValue("slabId"), resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to release the slab.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
