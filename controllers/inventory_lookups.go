package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/cache"
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
type InventoryLookupOps struct {
	// reads caches the vocabulary payloads per tenant. The permission check
	// still runs on every request; only the DB read is skipped. Any write
	// through this handler group drops the tenant's entries.
	reads *cache.TTLCache[string, map[string]any]
}

const (
	// lookupCacheTTL is the backstop for writes made on another instance.
	lookupCacheTTL = 60 * time.Second
	// lookupCacheMax bounds cached payloads (tenants x kinds x inactive flag).
	lookupCacheMax = 2000
	lookupCacheKey = "inventory-lookups"
)

// NewInventoryLookupOps constructs the handler group.
func NewInventoryLookupOps() *InventoryLookupOps {
	return &InventoryLookupOps{reads: cache.NewWithMax[string, map[string]any](lookupCacheTTL, lookupCacheMax)}
}

// cachedRead serves a tenant-scoped vocabulary read through the cache; parts
// distinguish variants (kind, includeInactive). An unresolved tenant bypasses
// the cache rather than risk a shared key.
func (h *InventoryLookupOps) cachedRead(ctx context.Context, parts []string, load func() (map[string]any, error)) (map[string]any, error) {
	t, err := tenancy.TenantFromContext(ctx)
	if err != nil || h.reads == nil {
		return load()
	}
	key := cache.TenantKey("", t.ID, lookupCacheKey, parts...)
	return h.reads.GetOrLoad(key, load)
}

// invalidate drops every cached vocabulary payload for the request's tenant.
func (h *InventoryLookupOps) invalidate(ctx context.Context) {
	t, err := tenancy.TenantFromContext(ctx)
	if err != nil || h.reads == nil {
		return
	}
	prefix := cache.TenantKey("", t.ID, lookupCacheKey)
	h.reads.DeleteFunc(func(k string) bool { return strings.HasPrefix(k, prefix) })
}

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
	out, err := h.cachedRead(r.Context(), []string{"all"}, func() (map[string]any, error) {
		return inventory.AllLookups(r.Context(), pool)
	})
	if err != nil {
		inventoryFail(w, err, "Failed to load inventory lookups.")
		return
	}
	// Copy: the cached map is shared and must stay read-only.
	resp := make(map[string]any, len(out)+1)
	for k, v := range out {
		resp[k] = v
	}
	resp["success"] = true
	writeJSON(w, http.StatusOK, resp)
}

// List GET /api/tenant/inventory/lookups/{kind}
func (h *InventoryLookupOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authLookup(w, r, authz.ActionRead)
	if !ok {
		return
	}
	includeInactive := r.URL.Query().Get("includeInactive") == "true"
	kind := r.PathValue("kind")
	out, err := h.cachedRead(r.Context(), []string{kind, strconv.FormatBool(includeInactive)}, func() (map[string]any, error) {
		items, err := inventory.ListLookup(r.Context(), pool, kind, includeInactive)
		if err != nil {
			return nil, err
		}
		return map[string]any{"success": true, "records": items}, nil
	})
	if err != nil {
		inventoryFail(w, err, "Failed to load lookup.")
		return
	}
	writeJSON(w, http.StatusOK, out)
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
	h.invalidate(r.Context())
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
	err = inventory.UpdateLookup(r.Context(), pool, r.PathValue("kind"), id, in, resolveEmployeeID(r, identityID))
	h.invalidate(r.Context())
	if err != nil {
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
	err = inventory.DeleteLookup(r.Context(), pool, r.PathValue("kind"), id, resolveEmployeeID(r, identityID))
	h.invalidate(r.Context())
	if err != nil {
		inventoryFail(w, err, "Failed to delete lookup entry.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Lookup entry deleted."})
}
