// controllers/estimate.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/estimate"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// EstimateOps handles the Estimate endpoints: a dedicated relational module
// (header + line items), a sibling of the Sales Order/Invoice modules — not
// served through the generic /api/tenant/crm/{workflowKey} JSONB router
// (spec AD-1). Mirrors SalesOrderOps' auth/IDOR/error-mapping conventions.
//
// Routes:
//
//	GET    /api/tenant/estimates                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/estimates/search              — filter + sort + search + pagination
//	POST   /api/tenant/estimates                     — create
//	GET    /api/tenant/estimates/{uuid}              — get (+ items)
//	PATCH  /api/tenant/estimates/{uuid}               — update
//	DELETE /api/tenant/estimates/{uuid}               — soft delete
//	POST   /api/tenant/estimates/{uuid}/transition    — status change
//	POST   /api/tenant/estimates/{uuid}/approve       — approval sign-off
//	GET    /api/tenant/estimates/{uuid}/audit         — audit trail
type EstimateOps struct{}

// NewEstimateOps constructs the handler group.
func NewEstimateOps() *EstimateOps { return &EstimateOps{} }

// authEstimate resolves JWT + tenant pool + the estimate:<action> RBAC grant
// for requests with no specific record yet (list/search/create).
func (h *EstimateOps) authEstimate(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceEstimate, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" estimates.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authEstimateByUUID resolves auth for a single-record action, then enforces
// the row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *EstimateOps) authEstimateByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *estimate.Estimate, bool) {
	pool, identityID, scope, ok := h.authEstimate(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	est, err := estimate.Get(r.Context(), pool, uuid)
	if errors.Is(err, estimate.ErrNotFound) {
		fail(w, http.StatusNotFound, "Estimate not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load estimate.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, est.OwnerUserID, "")
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", "estimate",
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Estimate not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, est, true
}

// estimateFail maps a store error to an HTTP response.
func estimateFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, estimate.ErrNotFound):
		fail(w, http.StatusNotFound, "Estimate not found.")
	case errors.Is(err, estimate.ErrInvalidTransition),
		errors.Is(err, estimate.ErrApprovalRequired),
		errors.Is(err, estimate.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, estimate.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case estimate.IsClientError(err):
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

// ---- list / search / create --------------------------------------------------

// List GET /api/tenant/estimates
func (h *EstimateOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authEstimate(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/estimates/search
func (h *EstimateOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authEstimate(w, r, authz.ActionRead)
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

func (h *EstimateOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := estimate.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		estimateFail(w, err, "Failed to search estimates.")
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

// Create POST /api/tenant/estimates
func (h *EstimateOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authEstimate(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in estimate.CreateEstimateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	est, err := estimate.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		estimateFail(w, err, "Failed to create estimate.")
		return
	}
	auditEstimate(r, pool, identityID, "create", est.ID, nil, est)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "estimate": est})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/estimates/{uuid}
func (h *EstimateOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, est, ok := h.authEstimateByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "estimate": est})
}

// Update PATCH /api/tenant/estimates/{uuid}
func (h *EstimateOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in estimate.UpdateEstimateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := estimate.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		estimateFail(w, err, "Failed to update estimate.")
		return
	}
	auditEstimate(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "estimate": after})
}

// Delete DELETE /api/tenant/estimates/{uuid}
func (h *EstimateOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := estimate.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		estimateFail(w, err, "Failed to delete estimate.")
		return
	}
	auditEstimateDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Estimate deleted."})
}

// Transition POST /api/tenant/estimates/{uuid}/transition  body {"toStatusCode":"..."}
func (h *EstimateOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		ToStatusCode string `json:"toStatusCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	est, err := estimate.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		estimateFail(w, err, "Failed to apply transition.")
		return
	}
	auditEstimate(r, pool, identityID, "transition", uuid, nil, est)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "estimate": est})
}

// Approve POST /api/tenant/estimates/{uuid}/approve
func (h *EstimateOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	est, err := estimate.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, estimate.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		estimateFail(w, err, "Failed to approve estimate.")
		return
	}
	auditEstimate(r, pool, identityID, "approve", uuid, nil, est)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "estimate": est})
}
