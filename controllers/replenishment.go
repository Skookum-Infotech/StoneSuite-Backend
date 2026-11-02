package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/replenishment"
	"stonesuite-backend/tenancy"
)

// ReplenishmentOps serves the Reorder page's backend: GET /api/tenant/replenishment
// (the items that will run short) and POST /api/tenant/replenishment/draft
// (turn one item's per-vendor split into draft purchase orders).
//
// There is no per-row owner to scope a reorder row by -- like inventory_item,
// it's tenant-wide reference data -- so replenishment:read/update is only ever
// meaningful at "all" scope; a grant at any narrower scope is treated as no
// grant at all. A row's open purchase orders do have an owner scope elsewhere
// in the app (purchase_order_owner_id), so List separately checks the caller's
// own purchase_order:read scope and leaves the order list off when it is
// narrower than "all". The on-order TOTAL stays: it is part of the item-level
// figure everyone who can see the row is entitled to, and without it the
// "needed" number would not add up.
type ReplenishmentOps struct{}

// authorize resolves who is asking and checks the given permission, writing
// the HTTP response itself on any failure. Because this resource has no
// legitimate "own" or "team" grant, a scope narrower than "all" is refused here
// too, so no caller has to remember to check it.
func (ReplenishmentOps) authorize(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceReplenishment, action)
	if err != nil {
		slog.Error("replenishment permission check", "error", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed || decision.Scope != authz.ScopeAll {
		reason := "not_allowed"
		if decision.Allowed {
			reason = "scope_not_all"
		}
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceReplenishment), "action", string(action), "reason", reason)
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" reorder suggestions.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// loadTimeout bounds one list: it scans the whole catalogue's ledgers, so it is
// given real headroom, but still far under the request's own deadline.
const loadTimeout = 60 * time.Second

// List handles GET /api/tenant/replenishment.
func (h ReplenishmentOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authorize(w, r, authz.ActionRead)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), loadTimeout)
	defer cancel()
	result, err := replenishment.LoadItems(ctx, pool, time.Now())
	if err != nil {
		slog.Error("load reorder items", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load reorder suggestions.")
		return
	}
	poDecision, err := authz.Check(r.Context(), pool, identityID, authz.ResourcePurchaseOrder, authz.ActionRead)
	if err != nil {
		slog.Error("replenishment purchase_order scope check", "error", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !poDecision.Allowed || poDecision.Scope != authz.ScopeAll {
		for i := range result.Items {
			result.Items[i].OpenOrders = nil
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "months": result.Months, "items": result.Items})
}

// maxDraftBodyBytes caps the Draft request body (an item uuid and up to
// MaxDraftVendors vendor/quantity pairs -- never large).
const maxDraftBodyBytes = 16 << 10

// Draft handles POST /api/tenant/replenishment/draft -- the buyer's "Create
// PO" button. It needs replenishment:update AND purchase_order:create (a real
// purchase order is being created); see the ResourceReplenishment catalog
// comment.
func (h ReplenishmentOps) Draft(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authorize(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	poDecision, err := authz.Check(r.Context(), pool, identityID, authz.ResourcePurchaseOrder, authz.ActionCreate)
	if err != nil {
		slog.Error("replenishment draft permission check", "error", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !poDecision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourcePurchaseOrder), "action", string(authz.ActionCreate))
		fail(w, http.StatusForbidden, "You do not have permission to create purchase orders.")
		return
	}

	var body replenishment.DraftInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDraftBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	employeeID := resolveEmployeeID(r, identityID)
	drafts, err := replenishment.CreateDrafts(r.Context(), pool, body, employeeID)
	var (
		reorderErr replenishment.ClientError
		poErr      purchaseorder.ClientError
	)
	switch {
	case errors.Is(err, replenishment.ErrNotFound):
		fail(w, http.StatusNotFound, "Item not found.")
		return
	case errors.As(err, &reorderErr):
		fail(w, http.StatusBadRequest, reorderErr.Error())
		return
	case errors.As(err, &poErr):
		fail(w, http.StatusBadRequest, poErr.Error())
		return
	case err != nil:
		slog.Error("create reorder draft purchase orders", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to create the draft purchase orders.")
		return
	}

	for _, d := range drafts {
		purchaseorder.NotifyDraftCreated(r.Context(), pool, d.Ref(), employeeID)
	}
	tenantID := ""
	if t, terr := tenancy.TenantFromContext(r.Context()); terr == nil {
		tenantID = t.ID
	}
	slog.Info("replenishment_draft", "tenant_id", tenantID, "identity", identityID,
		"item", body.InventoryItemID, "purchase_orders", len(drafts))
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "purchaseOrders": drafts})
}
