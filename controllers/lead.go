package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/lead"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LeadOps handles all /api/tenant/leads routes. Authorization uses the
// lead resource so it can be granted independently of prospects.
type LeadOps struct{}

// NewLeadOps constructs the handler group.
func NewLeadOps() *LeadOps { return &LeadOps{} }

// authLead resolves JWT + tenant pool + RBAC. Returns pool, identityID, ok.
func authLead(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceLead, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" leads.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// ListLeads GET /api/tenant/leads
func (h *LeadOps) ListLeads(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := authLead(w, r, authz.ActionRead)
	if !ok {
		return
	}
	leads, err := lead.List(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list leads.")
		return
	}
	if leads == nil {
		leads = []lead.Lead{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "leads": leads})
}

// CreateLead POST /api/tenant/leads
func (h *LeadOps) CreateLead(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := authLead(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	ownerUserID, _ := lead.UserIDByIdentity(r.Context(), pool, identityID)
	l := lead.FromMap(ownerUserID, body)

	created, err := lead.Create(r.Context(), pool, l)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create lead.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "lead": created})
}

// GetLead GET /api/tenant/leads/{id}
func (h *LeadOps) GetLead(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := authLead(w, r, authz.ActionRead)
	if !ok {
		return
	}
	l, err := lead.Get(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, lead.ErrNotFound) {
		fail(w, http.StatusNotFound, "Lead not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load lead.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "lead": l})
}

// UpdateLead PATCH /api/tenant/leads/{id}
func (h *LeadOps) UpdateLead(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := authLead(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	ownerUserID, _ := lead.UserIDByIdentity(r.Context(), pool, identityID)
	l := lead.FromMap(ownerUserID, body)

	updated, err := lead.Update(r.Context(), pool, r.PathValue("id"), l)
	if errors.Is(err, lead.ErrNotFound) {
		fail(w, http.StatusNotFound, "Lead not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update lead.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "lead": updated})
}

// DeleteLead DELETE /api/tenant/leads/{id}
func (h *LeadOps) DeleteLead(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := authLead(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := lead.Delete(r.Context(), pool, r.PathValue("id")); err != nil {
		if errors.Is(err, lead.ErrNotFound) {
			fail(w, http.StatusNotFound, "Lead not found.")
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to delete lead.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Lead deleted."})
}
