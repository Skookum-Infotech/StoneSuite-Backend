package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// FabricationOps handles the Fabrication & Installation endpoints: a production
// job spawned from a sales order (header + pieces + slab allocations + a 16-step
// checklist). Guarded by authz.ResourceInstallation; slab routes additionally
// require inventory_item. Mirrors SalesOrderOps' auth/IDOR/error conventions.
//
// Routes:
//
//	GET    /api/tenant/fabrication-jobs                         — list (cursor-paginated)
//	POST   /api/tenant/fabrication-jobs/search                  — filter + sort + search
//	POST   /api/tenant/fabrication-jobs                         — create (from a sales order)
//	POST   /api/tenant/sales-orders/{uuid}/fabricate            — spawn a job from a sales order
//	GET    /api/tenant/fabrication-jobs/{uuid}                  — get (+ pieces + steps)
//	PATCH  /api/tenant/fabrication-jobs/{uuid}                  — update header
//	DELETE /api/tenant/fabrication-jobs/{uuid}                  — soft delete (draft/cancelled only)
//	PUT    /api/tenant/fabrication-jobs/{uuid}/fabrication/status — status change
//	POST   /api/tenant/fabrication-jobs/{uuid}/hold             — put on hold
//	POST   /api/tenant/fabrication-jobs/{uuid}/resume           — resume from hold
//	POST   /api/tenant/fabrication-jobs/{uuid}/approve          — approval sign-off
//	GET    /api/tenant/fabrication-jobs/{uuid}/steps            — list checklist steps
//	PATCH  /api/tenant/fabrication-jobs/{uuid}/steps/{stepCode} — update a step
//	GET    /api/tenant/fabrication-jobs/{uuid}/slabs            — allocated slabs (needs inventory_item:read)
//	POST   /api/tenant/fabrication-jobs/{uuid}/slabs            — allocate a slab (needs inventory_item:update)
//	DELETE /api/tenant/fabrication-jobs/{uuid}/slabs/{slabUuid} — deallocate (needs inventory_item:update)
//	POST   /api/tenant/fabrication-jobs/{uuid}/slabs/{slabUuid}/disposition — declare fate on cancel
//	POST   /api/tenant/fabrication-jobs/{uuid}/pieces            — add a piece (before Cutting only)
//	PATCH  /api/tenant/fabrication-jobs/{uuid}/pieces/{pieceUuid} — edit a piece (before Cutting only)
//	DELETE /api/tenant/fabrication-jobs/{uuid}/pieces/{pieceUuid} — remove a piece (before Cutting only)
type FabricationOps struct{}

// NewFabricationOps constructs the handler group.
func NewFabricationOps() *FabricationOps { return &FabricationOps{} }

// authFJ resolves JWT + tenant pool + the installation:<action> grant for
// requests with no specific record yet (list/search/create).
func (h *FabricationOps) authFJ(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceInstallation, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInstallation), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" fabrication jobs.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authFJByUUID resolves auth for a single-record action, then enforces the
// row-level IDOR guard: an own-scoped caller may only act on jobs they own.
// Denial returns 404 (not 403) so callers cannot enumerate ids — mirrors
// authSOByUUID. Applies to every single-record route (steps, slabs, hold,
// resume, approve included).
func (h *FabricationOps) authFJByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *fabrication.Job, bool) {
	pool, identityID, scope, ok := h.authFJ(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	job, err := fabrication.Get(r.Context(), pool, uuid)
	if errors.Is(err, fabrication.ErrNotFound) {
		fail(w, http.StatusNotFound, "Fabrication job not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load fabrication job.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, job.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", "installation",
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Fabrication job not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, job, true
}

// requireInventory enforces the second grant that slab routes need: touching the
// inventory domain requires inventory_item in addition to installation (§3.1).
func (h *FabricationOps) requireInventory(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, action authz.Action) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInventoryItem, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInventoryItem), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to access inventory slabs.")
		return false
	}
	return true
}

// fjFail maps a store error to an HTTP response.
func fjFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, fabrication.ErrNotFound):
		fail(w, http.StatusNotFound, "Fabrication job not found.")
	case errors.Is(err, fabrication.ErrInvalidTransition),
		errors.Is(err, fabrication.ErrApprovalRequired),
		errors.Is(err, fabrication.ErrApprovalNotRequired),
		errors.Is(err, fabrication.ErrDispositionRequired),
		errors.Is(err, fabrication.ErrPiecesLocked),
		errors.Is(err, fabrication.ErrPieceHasSlabs):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, fabrication.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case fabrication.IsClientError(err):
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

// ---- list / search / create ------------------------------------------------

// List GET /api/tenant/fabrication-jobs
func (h *FabricationOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authFJ(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/fabrication-jobs/search
func (h *FabricationOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authFJ(w, r, authz.ActionRead)
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
	h.search(w, r, pool, identityID, scope, req)
}

func (h *FabricationOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := fabrication.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		fjFail(w, err, "Failed to search fabrication jobs.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"scope":      scope,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// Create POST /api/tenant/fabrication-jobs
func (h *FabricationOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authFJ(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in fabrication.CreateJobInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	job, err := fabrication.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		fjFail(w, err, "Failed to create fabrication job.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "fabricationJob": job})
}

// Fabricate POST /api/tenant/sales-orders/{uuid}/fabricate — spawn a job from an
// existing sales order. Requires installation:create (checked here) and reads a
// sales order (the store validates it exists and is live).
func (h *FabricationOps) Fabricate(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authFJ(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in fabrication.CreateJobInput
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	in.SalesOrderUUID = r.PathValue("uuid")
	job, err := fabrication.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		fjFail(w, err, "Failed to create fabrication job.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "fabricationJob": job})
}

// ---- single record ---------------------------------------------------------

// Get GET /api/tenant/fabrication-jobs/{uuid}
func (h *FabricationOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, job, ok := h.authFJByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "fabricationJob": job})
}

// Update PATCH /api/tenant/fabrication-jobs/{uuid}
func (h *FabricationOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in fabrication.UpdateJobInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	job, err := fabrication.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		fjFail(w, err, "Failed to update fabrication job.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "fabricationJob": job})
}

// Delete DELETE /api/tenant/fabrication-jobs/{uuid}
func (h *FabricationOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := fabrication.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		fjFail(w, err, "Failed to delete fabrication job.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Fabrication job deleted."})
}
