package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// InventoryOps handles the Inventory item-catalog endpoints. Inventory is
// shared, tenant-global reference data (like the lkp_* lookups) rather than an
// owner-scoped CRM record, so — unlike CRMOps — there is no per-record IDOR
// scope check beyond the resource-level inventory_item:<action> permission.
//
// Routes:
//
//	GET    /api/tenant/inventory/items            — unfiltered list (cursor-paginated)
//	POST   /api/tenant/inventory/items/search      — filter + sort + search + pagination
//	POST   /api/tenant/inventory/items             — create item
//	GET    /api/tenant/inventory/items/{uuid}       — get item
//	PATCH  /api/tenant/inventory/items/{uuid}       — update item
//	DELETE /api/tenant/inventory/items/{uuid}       — soft delete item
type InventoryOps struct{}

// NewInventoryOps constructs the handler group.
func NewInventoryOps() *InventoryOps { return &InventoryOps{} }

// authInventory resolves JWT + tenant pool + the inventory_item:<action> RBAC
// grant. Returns pool, identityID, ok.
func (h *InventoryOps) authInventory(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryItem, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory items.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// inventoryFail maps a store error to an HTTP response.
func inventoryFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, inventory.ErrNotFound):
		fail(w, http.StatusNotFound, "Item not found.")
	case inventory.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// List GET /api/tenant/inventory/items — the unfiltered default list, built
// from query params (?limit=&cursor=&search=) rather than a JSON body.
func (h *InventoryOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authInventory(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, req)
}

// Search POST /api/tenant/inventory/items/search — full filter + sort +
// global search + keyset pagination.
func (h *InventoryOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authInventory(w, r, authz.ActionRead)
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

func (h *InventoryOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, req query.Request) {
	page, err := inventory.Search(r.Context(), pool, req)
	if err != nil {
		inventoryFail(w, err, "Failed to search inventory items.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// Create POST /api/tenant/inventory/items
func (h *InventoryOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authInventory(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventory.CreateItemInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	item, err := inventory.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, "Failed to create item.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "item": item})
}

// Get GET /api/tenant/inventory/items/{uuid}
func (h *InventoryOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authInventory(w, r, authz.ActionRead)
	if !ok {
		return
	}
	item, err := inventory.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load item.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "item": item})
}

// Update PATCH /api/tenant/inventory/items/{uuid}
func (h *InventoryOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authInventory(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.CreateItemInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventory.Update(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to update item.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Item updated."})
}

// Delete DELETE /api/tenant/inventory/items/{uuid}
func (h *InventoryOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authInventory(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := inventory.SoftDelete(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to delete item.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Item deleted."})
}
