package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/query"
)

// Kit endpoints. A kit is catalogue data — a named group of items sold together —
// so it is governed by the same inventory_item:<action> grant as the items it
// holds: whoever may create an item may create a kit, with no extra role row.
//
// Routes:
//
//	GET    /api/tenant/inventory/kits           — unfiltered list (cursor-paginated)
//	POST   /api/tenant/inventory/kits/search     — filter + sort + search + pagination
//	POST   /api/tenant/inventory/kits            — create kit (with its items)
//	GET    /api/tenant/inventory/kits/{uuid}      — get kit with its items
//	PATCH  /api/tenant/inventory/kits/{uuid}      — update kit
//	DELETE /api/tenant/inventory/kits/{uuid}      — soft delete kit

// kitFail maps a kit store error to an HTTP response. A missing kit reads
// "Kit not found." where the item mapping would say "Item"; every other error
// (duplicate name, bad input, bad filter) is the shared mapping.
func kitFail(w http.ResponseWriter, err error, serverMsg string) {
	if errors.Is(err, inventory.ErrNotFound) {
		fail(w, http.StatusNotFound, "Kit not found.")
		return
	}
	inventoryFail(w, err, serverMsg)
}

// ListKits GET /api/tenant/inventory/kits — the unfiltered default list, built
// from query params (?limit=&cursor=&search=).
func (h *InventoryOps) ListKits(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authInventory(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.searchKits(w, r, pool, req)
}

// SearchKits POST /api/tenant/inventory/kits/search — full filter + sort +
// global search + keyset pagination.
func (h *InventoryOps) SearchKits(w http.ResponseWriter, r *http.Request) {
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
	h.searchKits(w, r, pool, req)
}

func (h *InventoryOps) searchKits(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, req query.Request) {
	page, err := inventory.SearchKits(r.Context(), pool, req)
	if err != nil {
		kitFail(w, err, "Failed to search kits.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// CreateKit POST /api/tenant/inventory/kits
func (h *InventoryOps) CreateKit(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authInventory(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventory.KitInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	kit, err := inventory.CreateKit(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		kitFail(w, err, "Failed to create kit.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "kit": kit})
}

// GetKit GET /api/tenant/inventory/kits/{uuid}
func (h *InventoryOps) GetKit(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authInventory(w, r, authz.ActionRead)
	if !ok {
		return
	}
	kit, err := inventory.GetKit(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		kitFail(w, err, "Failed to load kit.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "kit": kit})
}

// UpdateKit PATCH /api/tenant/inventory/kits/{uuid}
func (h *InventoryOps) UpdateKit(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authInventory(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.KitInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventory.UpdateKit(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		kitFail(w, err, "Failed to update kit.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Kit updated."})
}

// DeleteKit DELETE /api/tenant/inventory/kits/{uuid}
func (h *InventoryOps) DeleteKit(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authInventory(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := inventory.SoftDeleteKit(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		kitFail(w, err, "Failed to delete kit.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Kit deleted."})
}
