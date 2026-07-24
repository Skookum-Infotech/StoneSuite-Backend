package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// InventorySlabOps handles the serialized slab catalog under
// /api/tenant/inventory/slabs. Guarded by authz.ResourceInventoryItem — a slab
// is a physical instance of an inventory item.
type InventorySlabOps struct{}

// NewInventorySlabOps constructs the handler group.
func NewInventorySlabOps() *InventorySlabOps { return &InventorySlabOps{} }

func (h *InventorySlabOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryItem), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory slabs.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

func slabFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, fabrication.ErrNotFound):
		fail(w, http.StatusNotFound, "Slab not found.")
	case fabrication.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// Create POST /api/tenant/inventory/slabs — receive a physical slab.
func (h *InventorySlabOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in fabrication.CreateSlabInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	slab, err := fabrication.CreateSlab(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		slabFail(w, err, "Failed to create slab.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "slab": slab})
}

// Get GET /api/tenant/inventory/slabs/{uuid}
func (h *InventorySlabOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	slab, err := fabrication.GetSlab(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		slabFail(w, err, "Failed to load slab.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "slab": slab})
}

// Scrap POST /api/tenant/inventory/slabs/{uuid}/scrap
func (h *InventorySlabOps) Scrap(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	if err := fabrication.ScrapSlab(r.Context(), pool, r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		slabFail(w, err, "Failed to scrap slab.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Slab scrapped."})
}
