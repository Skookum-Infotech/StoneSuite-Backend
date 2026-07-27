package controllers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// InventoryLookupOps serves the inventory vocabularies — units, warehouses,
// materials, colours, finishes, tax rates and reason codes.
//
// Before these routes existed nothing in the app returned lkp_unit or
// lkp_warehouse, so an item form could not populate its unit dropdown even
// though inventory_item_unit_id is NOT NULL.
//
// Routes:
//
//	GET    /api/tenant/inventory/lookups              — every vocabulary at once
//	GET    /api/tenant/inventory/lookups/{kind}       — one vocabulary
//	POST   /api/tenant/inventory/lookups/{kind}       — add an entry
//	PATCH  /api/tenant/inventory/lookups/{kind}/{id}  — edit an entry
//	DELETE /api/tenant/inventory/lookups/{kind}/{id}  — soft-delete an entry
type InventoryLookupOps struct{}

// NewInventoryLookupOps constructs the handler group.
func NewInventoryLookupOps() *InventoryLookupOps { return &InventoryLookupOps{} }

// authLookup resolves JWT + tenant pool + the inventory_lookup:<action> grant.
//
// Read is deliberately its own permission rather than riding on
// inventory_item:read: a bin clerk who may not touch the catalogue still needs
// the vocabularies to fill in a bin or unit form.
func (h *InventoryLookupOps) authLookup(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryLookup, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryLookup), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory lookups.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// All GET /api/tenant/inventory/lookups
func (h *InventoryLookupOps) All(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authLookup(w, r, authz.ActionRead)
	if !ok {
		return
	}
	out, err := inventory.AllLookups(r.Context(), pool)
	if err != nil {
		inventoryFail(w, err, "Failed to load inventory lookups.")
		return
	}
	out["success"] = true
	writeJSON(w, http.StatusOK, out)
}

// List GET /api/tenant/inventory/lookups/{kind}
func (h *InventoryLookupOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authLookup(w, r, authz.ActionRead)
	if !ok {
		return
	}
	includeInactive := r.URL.Query().Get("includeInactive") == "true"
	items, err := inventory.ListLookup(r.Context(), pool, r.PathValue("kind"), includeInactive)
	if err != nil {
		inventoryFail(w, err, "Failed to load lookup.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": items})
}

// Create POST /api/tenant/inventory/lookups/{kind}
func (h *InventoryLookupOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authLookup(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventory.LookupInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	item, err := inventory.CreateLookup(r.Context(), pool, r.PathValue("kind"), in, resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, "Failed to create lookup entry.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "record": item})
}

// Update PATCH /api/tenant/inventory/lookups/{kind}/{id}
func (h *InventoryLookupOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authLookup(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		// Not a 400: a non-numeric id simply identifies nothing, and 404 keeps
		// it indistinguishable from an id that does not exist.
		fail(w, http.StatusNotFound, "Lookup entry not found.")
		return
	}
	var in inventory.LookupInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventory.UpdateLookup(r.Context(), pool, r.PathValue("kind"), id, in, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to update lookup entry.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Lookup entry updated."})
}

// Delete DELETE /api/tenant/inventory/lookups/{kind}/{id}
func (h *InventoryLookupOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authLookup(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "Lookup entry not found.")
		return
	}
	if err := inventory.DeleteLookup(r.Context(), pool, r.PathValue("kind"), id, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to delete lookup entry.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Lookup entry deleted."})
}
