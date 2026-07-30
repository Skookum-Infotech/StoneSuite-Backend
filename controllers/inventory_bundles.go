package controllers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// InventoryBundleOps serves bundles — sets of slabs banded to one pallet, sawn
// from the same block and handled together until the band is cut.
//
// A bundle carries no stock of its own; only its member units do. Every route
// here is therefore stock-neutral and writes no ledger row.
//
// Routes:
//
//	GET    /api/tenant/inventory/bundles                 — list (?warehouseId=&status=)
//	POST   /api/tenant/inventory/bundles                 — create (optionally with members)
//	GET    /api/tenant/inventory/bundles/{uuid}          — get
//	PATCH  /api/tenant/inventory/bundles/{uuid}          — update paperwork
//	DELETE /api/tenant/inventory/bundles/{uuid}          — soft delete (must be empty)
//	GET    /api/tenant/inventory/bundles/{uuid}/members  — the units on the pallet
//	POST   /api/tenant/inventory/bundles/{uuid}/members  — attach units
//	DELETE /api/tenant/inventory/bundles/{uuid}/members  — detach units
//	POST   /api/tenant/inventory/bundles/{uuid}/seal     — band it
//	POST   /api/tenant/inventory/bundles/{uuid}/break    — cut the band, release members
//	PATCH  /api/tenant/inventory/bundles/{uuid}/bin      — move the pallet and its units
type InventoryBundleOps struct{}

// NewInventoryBundleOps constructs the handler group.
func NewInventoryBundleOps() *InventoryBundleOps { return &InventoryBundleOps{} }

func (h *InventoryBundleOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryBundle, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryBundle), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory bundles.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// authByUUID adds the existence guard for single-record MUTATIONS, 404ing so
// bundle ids cannot be enumerated.
//
// Read handlers deliberately do not call it: GetBundle already returns
// ErrNotFound for a missing, soft-deleted or malformed uuid and inventoryFail
// maps that to 404, so the guard would fetch the same row twice. Bundles are
// tenant-global with no owner column, so a read leaks no ownership state.
func (h *InventoryBundleOps) authByUUID(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	pool, identityID, ok := h.auth(w, r, action)
	if !ok {
		return nil, "", false
	}
	if _, err := inventory.GetBundle(r.Context(), pool, r.PathValue("uuid")); err != nil {
		inventoryFail(w, err, "Failed to load bundle.")
		return nil, "", false
	}
	return pool, identityID, true
}

// List GET /api/tenant/inventory/bundles
func (h *InventoryBundleOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	bundles, err := inventory.ListBundles(r.Context(), pool,
		r.URL.Query().Get("warehouseId"), r.URL.Query().Get("status"))
	if err != nil {
		inventoryFail(w, err, "Failed to list bundles.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": bundles})
}

// Get GET /api/tenant/inventory/bundles/{uuid}
func (h *InventoryBundleOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	bundle, err := inventory.GetBundle(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load bundle.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "bundle": bundle})
}

// Members GET /api/tenant/inventory/bundles/{uuid}/members
func (h *InventoryBundleOps) Members(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	units, err := inventory.BundleMembers(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load bundle members.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": units})
}

// Create POST /api/tenant/inventory/bundles
func (h *InventoryBundleOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventory.BundleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	bundle, err := inventory.CreateBundle(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, "Failed to create bundle.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "bundle": bundle})
}

// Update PATCH /api/tenant/inventory/bundles/{uuid}
func (h *InventoryBundleOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.BundleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventory.UpdateBundle(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to update bundle.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Bundle updated."})
}

// Delete DELETE /api/tenant/inventory/bundles/{uuid}
func (h *InventoryBundleOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := inventory.DeleteBundle(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to delete bundle.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Bundle deleted."})
}

// AddMembers POST /api/tenant/inventory/bundles/{uuid}/members
func (h *InventoryBundleOps) AddMembers(w http.ResponseWriter, r *http.Request) {
	h.members(w, r, inventory.AddBundleMembers, "Failed to add bundle members.")
}

// RemoveMembers DELETE /api/tenant/inventory/bundles/{uuid}/members
func (h *InventoryBundleOps) RemoveMembers(w http.ResponseWriter, r *http.Request) {
	h.members(w, r, inventory.RemoveBundleMembers, "Failed to remove bundle members.")
}

// members runs the shared decode/dispatch for the two membership endpoints.
// Both take update rather than create or delete: membership changes the bundle,
// and neither mints nor destroys a unit.
func (h *InventoryBundleOps) members(w http.ResponseWriter, r *http.Request,
	op func(ctx context.Context, pool *pgxpool.Pool, uuid string, in inventory.BundleMemberInput, actor int) (*inventory.Bundle, error),
	failMsg string) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.BundleMemberInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	bundle, err := op(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, failMsg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "bundle": bundle})
}

// Seal POST /api/tenant/inventory/bundles/{uuid}/seal
func (h *InventoryBundleOps) Seal(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	bundle, err := inventory.SealBundle(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, "Failed to seal bundle.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "bundle": bundle})
}

// Break POST /api/tenant/inventory/bundles/{uuid}/break
func (h *InventoryBundleOps) Break(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	bundle, err := inventory.BreakBundle(r.Context(), pool, r.PathValue("uuid"), body.Note, resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, "Failed to break bundle.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "bundle": bundle})
}

// MoveBin PATCH /api/tenant/inventory/bundles/{uuid}/bin — relocate the pallet
// and every unit on it. This is the legitimate way to move a sealed bundle.
func (h *InventoryBundleOps) MoveBin(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.MoveBundleInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventory.MoveBundle(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to move bundle.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Bundle moved."})
}
