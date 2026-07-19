// controllers/quote.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/quote"
	"stonesuite-backend/tenancy"
)

// QuoteOps handles the Quote endpoints: a dedicated relational module
// (header + line items), a sibling of the Sales Order/Invoice modules — not
// served through the generic /api/tenant/crm/{workflowKey} JSONB router
// (spec AD-1). Mirrors SalesOrderOps' auth/IDOR/error-mapping conventions.
//
// Routes:
//
//	GET    /api/tenant/quotes                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/quotes/search              — filter + sort + search + pagination
//	POST   /api/tenant/quotes                     — create
//	GET    /api/tenant/quotes/{uuid}              — get (+ items)
//	PATCH  /api/tenant/quotes/{uuid}               — update
//	DELETE /api/tenant/quotes/{uuid}               — soft delete
//	POST   /api/tenant/quotes/{uuid}/transition    — status change
//	POST   /api/tenant/quotes/{uuid}/approve       — approval sign-off
//	POST   /api/tenant/quotes/{uuid}/convert       — convert to a Sales Order
//	GET    /api/tenant/quotes/{uuid}/audit         — audit trail
type QuoteOps struct{}

// NewQuoteOps constructs the handler group.
func NewQuoteOps() *QuoteOps { return &QuoteOps{} }

// authQuote resolves JWT + tenant pool + the quote:<action> RBAC grant
// for requests with no specific record yet (list/search/create).
func (h *QuoteOps) authQuote(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceQuote, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceQuote), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" quotes.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authQuoteByUUID resolves auth for a single-record action, then enforces
// the row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *QuoteOps) authQuoteByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *quote.Quote, bool) {
	pool, identityID, scope, ok := h.authQuote(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	qte, err := quote.Get(r.Context(), pool, uuid)
	if errors.Is(err, quote.ErrNotFound) {
		fail(w, http.StatusNotFound, "Quote not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load quote.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, qte.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", "quote",
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Quote not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, qte, true
}

// quoteFail maps a store error to an HTTP response.
func quoteFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, quote.ErrNotFound):
		fail(w, http.StatusNotFound, "Quote not found.")
	case errors.Is(err, quote.ErrInvalidTransition),
		errors.Is(err, quote.ErrApprovalRequired),
		errors.Is(err, quote.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, quote.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case quote.IsClientError(err):
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

// List GET /api/tenant/quotes
func (h *QuoteOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authQuote(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/quotes/search
func (h *QuoteOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authQuote(w, r, authz.ActionRead)
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

func (h *QuoteOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := quote.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		quoteFail(w, err, "Failed to search quotes.")
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

// Create POST /api/tenant/quotes
func (h *QuoteOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authQuote(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in quote.CreateQuoteInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	est, err := quote.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		quoteFail(w, err, "Failed to create quote.")
		return
	}
	auditQuote(r, pool, identityID, "create", est.ID, nil, est)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "quote": est})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/quotes/{uuid}
func (h *QuoteOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, est, ok := h.authQuoteByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "quote": est})
}

// Update PATCH /api/tenant/quotes/{uuid}
func (h *QuoteOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in quote.UpdateQuoteInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := quote.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		quoteFail(w, err, "Failed to update quote.")
		return
	}
	auditQuote(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "quote": after})
}

// Delete DELETE /api/tenant/quotes/{uuid}
func (h *QuoteOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := quote.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		quoteFail(w, err, "Failed to delete quote.")
		return
	}
	auditQuoteDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Quote deleted."})
}

// Transition POST /api/tenant/quotes/{uuid}/transition  body {"toStatusCode":"..."}
func (h *QuoteOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionTransition)
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
	est, err := quote.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		quoteFail(w, err, "Failed to apply transition.")
		return
	}
	auditQuote(r, pool, identityID, "transition", uuid, nil, est)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "quote": est})
}

// Approve POST /api/tenant/quotes/{uuid}/approve
func (h *QuoteOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	est, err := quote.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, quote.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		quoteFail(w, err, "Failed to approve quote.")
		return
	}
	auditQuote(r, pool, identityID, "approve", uuid, nil, est)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "quote": est})
}
