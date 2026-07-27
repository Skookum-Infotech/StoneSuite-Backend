package controllers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// InventoryUnitOps handles serialized physical stock — individual slabs and the
// remnants cut from them.
//
// Guarded by authz.ResourceInventoryUnit rather than ResourceInventoryItem, so
// a yard clerk can be granted stock handling without catalogue edit rights.
// tenant/schema.sql carries a one-time backfill granting inventory_unit:<action>
// to every role that already held inventory_item:<action>, so no existing role
// loses access when this ships.
//
// Routes:
//
//	GET    /api/tenant/inventory/units                — list (cursor-paginated)
//	POST   /api/tenant/inventory/units/search         — filter + sort + search
//	GET    /api/tenant/inventory/units/remnants       — usable offcuts, largest first
//	POST   /api/tenant/inventory/units                — receive a unit
//	GET    /api/tenant/inventory/units/{uuid}         — get
//	PATCH  /api/tenant/inventory/units/{uuid}/bin     — move between bins
//	POST   /api/tenant/inventory/units/{uuid}/scrap   — write off
//	GET    /api/tenant/inventory/units/{uuid}/history — movement trail
//
// The older /api/tenant/inventory/slabs/* routes remain live and are served by
// the same handlers, so the frontend can migrate without a flag day.
type InventoryUnitOps struct{}

// NewInventoryUnitOps constructs the handler group.
func NewInventoryUnitOps() *InventoryUnitOps { return &InventoryUnitOps{} }

func (h *InventoryUnitOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInventoryUnit, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryUnit), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" inventory units.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// authByUUID adds the existence guard for single-record MUTATIONS, 404ing so
// unit ids cannot be enumerated.
//
// Read handlers deliberately do NOT call this. They load the record themselves,
// and GetUnit already returns ErrNotFound for a missing, soft-deleted or
// malformed uuid, which inventoryFail maps to 404 — so the guard would only
// fetch the same row twice. It is worth calling before a mutation because it
// puts the 404 ahead of any write attempt.
//
// This is safe here specifically because inventory is tenant-global reference
// data with no owner column: there is no "exists but you may not see it" state
// to leak. A module with per-row ownership must guard its reads too.
func (h *InventoryUnitOps) authByUUID(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	pool, identityID, ok := h.auth(w, r, action)
	if !ok {
		return nil, "", false
	}
	if _, err := inventory.GetUnit(r.Context(), pool, r.PathValue("uuid")); err != nil {
		inventoryFail(w, err, "Failed to load unit.")
		return nil, "", false
	}
	return pool, identityID, true
}

// List GET /api/tenant/inventory/units
func (h *InventoryUnitOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, req)
}

// Search POST /api/tenant/inventory/units/search
func (h *InventoryUnitOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
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

func (h *InventoryUnitOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, req query.Request) {
	page, err := inventory.SearchUnits(r.Context(), pool, req)
	if err != nil {
		inventoryFail(w, err, "Failed to search inventory units.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// Remnants GET /api/tenant/inventory/units/remnants — the offcut picker.
func (h *InventoryUnitOps) Remnants(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	minArea, _ := strconv.ParseFloat(r.URL.Query().Get("minArea"), 64)
	units, err := inventory.UsableRemnants(r.Context(), pool, r.URL.Query().Get("itemId"), minArea)
	if err != nil {
		inventoryFail(w, err, "Failed to load remnants.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": units})
}

// Create POST /api/tenant/inventory/units — receive a physical unit.
func (h *InventoryUnitOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.auth(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in inventory.CreateUnitInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	u, err := inventory.CreateUnit(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		inventoryFail(w, err, "Failed to create unit.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "unit": u})
}

// Get GET /api/tenant/inventory/units/{uuid}
func (h *InventoryUnitOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	u, err := inventory.GetUnit(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load unit.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "unit": u})
}

// MoveBin PATCH /api/tenant/inventory/units/{uuid}/bin — relocate within the
// warehouse. Stock-neutral, so it writes no ledger row.
func (h *InventoryUnitOps) MoveBin(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.MoveUnitInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := inventory.MoveUnitToBin(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to move unit.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Unit moved."})
}

// Scrap POST /api/tenant/inventory/units/{uuid}/scrap
func (h *InventoryUnitOps) Scrap(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var body struct {
		ReasonID *int   `json:"reasonId"`
		Note     string `json:"note"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	if err := inventory.ScrapUnit(r.Context(), pool, r.PathValue("uuid"), body.ReasonID, body.Note, resolveEmployeeID(r, identityID)); err != nil {
		inventoryFail(w, err, "Failed to scrap unit.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Unit scrapped."})
}

// History GET /api/tenant/inventory/units/{uuid}/history — the operational
// trail (bin moves, re-grades, cuts, scrap), distinct from the stock ledger.
func (h *InventoryUnitOps) History(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := inventory.UnitHistory(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		inventoryFail(w, err, "Failed to load unit history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": entries})
}
