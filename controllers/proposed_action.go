package controllers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"stonesuite-backend/middleware"
	"stonesuite-backend/proposedaction"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// maxProposedActionBodyBytes caps the (unused) confirm/cancel request body.
const maxProposedActionBodyBytes = 1 << 10

// Client-facing messages for proposed-action errors.
const (
	msgActionNotFound   = "Action not found."
	msgActionExpired    = "This proposal has expired."
	msgActionConflict   = "This proposal can no longer be changed."
	msgActionNotDone    = "The action could not be completed."
	msgActionNoProfile  = "No workspace profile"
	msgActionBadLimit   = "limit must be a positive integer."
	msgActionBadCursor  = "Invalid cursor."
	msgActionInternal   = "Failed to process the action."
	msgActionConfirmed  = "Action confirmed."
	msgActionReplayed   = "Action was already confirmed."
	eventActionIDOR     = "proposed_action_idor"
	eventActionViaLabel = "proposed_action_confirm"
)

// proposedActionStore is the persistence surface the handlers need.
type proposedActionStore interface {
	List(ctx context.Context, identityID string, limit int, cursor string) ([]proposedaction.Action, string, error)
	Get(ctx context.Context, identityID, id string) (proposedaction.Action, error)
	Cancel(ctx context.Context, identityID, id string) (proposedaction.Action, error)
	Confirm(ctx context.Context, caller proposedaction.Caller, id string) (proposedaction.Action, bool, error)
}

// proposedActionEnv holds the per-request tenant dependencies.
type proposedActionEnv struct {
	store  proposedActionStore
	userID func(ctx context.Context, identityID string) (string, error)
}

// ProposedActionOps serves /api/tenant/actions.
//
// Authorization model: list/get/cancel are own-rows-only by construction (the
// store filters on the caller's identity_id, so no cross-user access exists)
// and therefore carry no resource Require. Confirm re-checks the TARGET
// resource's permission and scope inside Store.Confirm under the caller's
// current roles, so a stale proposal can never widen access.
type ProposedActionOps struct {
	resolve func(r *http.Request) (proposedActionEnv, error)
}

// NewProposedActionOps builds the handler set; each request uses its own tenant pool.
func NewProposedActionOps(reg *proposedaction.Registry, ttl time.Duration) *ProposedActionOps {
	return &ProposedActionOps{resolve: func(r *http.Request) (proposedActionEnv, error) {
		pool, err := tenancy.PoolFromContext(r.Context())
		if err != nil {
			return proposedActionEnv{}, err
		}
		return proposedActionEnv{
			store: proposedaction.NewStore(pool, reg, ttl),
			userID: func(ctx context.Context, identityID string) (string, error) {
				return workflow.UserIDByIdentity(ctx, pool, identityID)
			},
		}, nil
	}}
}

func (h *ProposedActionOps) auth(w http.ResponseWriter, r *http.Request) (proposedActionEnv, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return proposedActionEnv{}, "", false
	}
	env, err := h.resolve(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return proposedActionEnv{}, "", false
	}
	return env, payload.ID, true
}

// writeActionError maps store errors to HTTP responses.
func writeActionError(w http.ResponseWriter, r *http.Request, identityID, id string, err error) {
	var nf *proposedaction.NotFoundError
	var forb *proposedaction.ForbiddenError
	var val *proposedaction.ValidationError
	switch {
	case errors.As(err, &nf):
		if nf.Mismatch {
			logSecurityEvent(r, eventActionIDOR, "identity", identityID, "action_id", id)
		}
		fail(w, http.StatusNotFound, msgActionNotFound)
	case errors.Is(err, proposedaction.ErrNotFound):
		fail(w, http.StatusNotFound, msgActionNotFound)
	case errors.Is(err, proposedaction.ErrExpired):
		fail(w, http.StatusGone, msgActionExpired)
	case errors.Is(err, proposedaction.ErrConflict):
		fail(w, http.StatusConflict, msgActionConflict)
	case errors.As(err, &forb):
		logSecurityEvent(r, "permission_denied", "identity", identityID,
			"resource", string(forb.Resource), "action", string(forb.Action), "via", eventActionViaLabel)
		fail(w, http.StatusForbidden, "You do not have permission to perform this action.")
	case errors.As(err, &val):
		fail(w, http.StatusUnprocessableEntity, val.Message)
	case errors.Is(err, proposedaction.ErrForbidden):
		fail(w, http.StatusForbidden, "You do not have permission to perform this action.")
	case errors.Is(err, proposedaction.ErrExecutionFailed):
		fail(w, http.StatusUnprocessableEntity, msgActionNotDone)
	case errors.Is(err, proposedaction.ErrInvalidCursor):
		fail(w, http.StatusBadRequest, msgActionBadCursor)
	default:
		slog.Error("proposed action", "action_id", id, "error", err)
		fail(w, http.StatusInternalServerError, msgActionInternal)
	}
}

// List handles GET /api/tenant/actions.
func (h *ProposedActionOps) List(w http.ResponseWriter, r *http.Request) {
	env, identityID, ok := h.auth(w, r)
	if !ok {
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			fail(w, http.StatusBadRequest, msgActionBadLimit)
			return
		}
		limit = n
	}
	actions, next, err := env.store.List(r.Context(), identityID, limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeActionError(w, r, identityID, "", err)
		return
	}
	if actions == nil {
		actions = []proposedaction.Action{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "actions": actions, "nextCursor": next})
}

// Get handles GET /api/tenant/actions/{id}.
func (h *ProposedActionOps) Get(w http.ResponseWriter, r *http.Request) {
	env, identityID, ok := h.auth(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	a, err := env.store.Get(r.Context(), identityID, id)
	if err != nil {
		writeActionError(w, r, identityID, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "action": a})
}

// Cancel handles POST /api/tenant/actions/{id}/cancel.
func (h *ProposedActionOps) Cancel(w http.ResponseWriter, r *http.Request) {
	env, identityID, ok := h.auth(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProposedActionBodyBytes)
	id := r.PathValue("id")
	a, err := env.store.Cancel(r.Context(), identityID, id)
	if err != nil {
		writeActionError(w, r, identityID, id, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "action": a})
}

// Confirm handles POST /api/tenant/actions/{id}/confirm.
func (h *ProposedActionOps) Confirm(w http.ResponseWriter, r *http.Request) {
	env, identityID, ok := h.auth(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxProposedActionBodyBytes)
	id := r.PathValue("id")
	userID, err := env.userID(r.Context(), identityID)
	if errors.Is(err, workflow.ErrNoTenantUser) || (err == nil && userID == "") {
		fail(w, http.StatusForbidden, msgActionNoProfile)
		return
	}
	if err != nil {
		slog.Error("proposed action: resolve user", "error", err)
		fail(w, http.StatusInternalServerError, msgActionInternal)
		return
	}
	a, replayed, err := env.store.Confirm(r.Context(), proposedaction.Caller{IdentityID: identityID, UserID: userID}, id)
	if err != nil {
		writeActionError(w, r, identityID, id, err)
		return
	}
	msg := msgActionConfirmed
	if replayed {
		msg = msgActionReplayed
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "action": a, "replayed": replayed, "message": msg})
}
