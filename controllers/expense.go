// controllers/expense.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/expense"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// ExpenseOps handles the Expense endpoints: a dedicated relational module
// (header + line items + configuration-driven approval + dedicated reject),
// employee self-service expense claims — not served through the generic
// /api/tenant/crm/{workflowKey} JSONB router. Auth/IDOR/error-mapping
// skeleton copied from controllers/requisition.go, itself mirroring
// controllers/payment.go (the only module that correctly logs
// permission_denied).
//
// Routes:
//
//	GET    /api/tenant/expenses                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/expenses/search             — filter + sort + search + pagination
//	POST   /api/tenant/expenses                    — create (claimant = caller)
//	GET    /api/tenant/expenses/categories         — active lkp_expense_category rows (line category picker)
//	GET    /api/tenant/expenses/{uuid}             — get (+ items)
//	PATCH  /api/tenant/expenses/{uuid}             — update (DRFT only)
//	DELETE /api/tenant/expenses/{uuid}             — soft delete (DRFT only)
//	POST   /api/tenant/expenses/{uuid}/transition  — status change (submit/recall/revise/reimburse/approve-after-quorum)
//	POST   /api/tenant/expenses/{uuid}/approve     — approval sign-off
//	POST   /api/tenant/expenses/{uuid}/reject      — reject (dedicated, always captures a reason)
//	GET    /api/tenant/expenses/{uuid}/audit       — audit trail
//
// Receipts have no routes here — they reuse the generic
// /api/tenant/records/{uuid}/attachments/* mechanism (controllers/attachments.go).
type ExpenseOps struct{}

// NewExpenseOps constructs the handler group.
func NewExpenseOps() *ExpenseOps { return &ExpenseOps{} }

// authExp resolves JWT + tenant pool + the expense:<action> RBAC grant for
// requests with no specific record yet (list/search/create), plus spec AD-8
// layer 1: a disabled tenant user may still hold an unrevoked role grant
// (authz.Check does not filter on user status), so every mutating action is
// blocked here explicitly. Read access stays open so a disabled user
// retains visibility into their own history.
func (h *ExpenseOps) authExp(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceExpense, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceExpense), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" expenses.")
		return nil, "", "", false
	}
	if action != authz.ActionRead {
		active, err := userstore.IsActiveIdentity(r.Context(), pool, payload.ID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", "", false
		}
		if !active {
			logSecurityEvent(r, "inactive_user_denied",
				"identity", payload.ID, "resource", string(authz.ResourceExpense), "action", string(action))
			fail(w, http.StatusForbidden, "Your account is inactive.")
			return nil, "", "", false
		}
	}
	return pool, payload.ID, decision.Scope, true
}

// authExpByUUID resolves auth for a single-record action, then enforces the
// row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *ExpenseOps) authExpByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *expense.Expense, bool) {
	pool, identityID, scope, ok := h.authExp(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	exp, err := expense.Get(r.Context(), pool, uuid)
	if errors.Is(err, expense.ErrNotFound) {
		fail(w, http.StatusNotFound, "Expense claim not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load expense claim.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, exp.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", string(authz.ResourceExpense),
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Expense claim not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, exp, true
}

// expFail maps a store error to an HTTP response.
func expFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, expense.ErrNotFound):
		fail(w, http.StatusNotFound, "Expense claim not found.")
	case errors.Is(err, expense.ErrInvalidTransition),
		errors.Is(err, expense.ErrApprovalRequired),
		errors.Is(err, expense.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, expense.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case expense.IsClientError(err):
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

// List GET /api/tenant/expenses
func (h *ExpenseOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authExp(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/expenses/search
func (h *ExpenseOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authExp(w, r, authz.ActionRead)
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

func (h *ExpenseOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := expense.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		expFail(w, err, "Failed to search expenses.")
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

// Categories GET /api/tenant/expenses/categories
// Lists active, non-deleted lkp_expense_category rows for the line item
// category picker. Gated by expense:read (same as List/Search/Get) since it
// only ever backs an expense-form dropdown, mirroring
// ChartOfAccountsOps.Categories's read-gated lookup shape.
func (h *ExpenseOps) Categories(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authExp(w, r, authz.ActionRead)
	if !ok {
		return
	}
	cats, err := queryLookupItems(r.Context(), pool, `
		SELECT expense_category_id, expense_category_code, expense_category_name
		FROM lkp_expense_category
		WHERE expense_category_is_active AND expense_category_deleted_at IS NULL
		ORDER BY expense_category_name`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load expense categories.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "categories": cats})
}

// Create POST /api/tenant/expenses
func (h *ExpenseOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authExp(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in expense.CreateExpenseInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	exp, err := expense.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		expFail(w, err, "Failed to create expense claim.")
		return
	}
	auditExp(r, pool, identityID, "create", exp.ID, nil, exp)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "expense": exp})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/expenses/{uuid}
func (h *ExpenseOps) Get(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, exp, ok := h.authExpByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		expFail(w, err, "Failed to load expense.")
		return
	}
	info, err := expense.GetApprovalInfo(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		expFail(w, err, "Failed to load expense.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "expense": exp, "approval": info})
}

// Update PATCH /api/tenant/expenses/{uuid}
func (h *ExpenseOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authExpByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in expense.UpdateExpenseInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := expense.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		expFail(w, err, "Failed to update expense claim.")
		return
	}
	auditExp(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "expense": after})
}

// Delete DELETE /api/tenant/expenses/{uuid}
func (h *ExpenseOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authExpByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := expense.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		expFail(w, err, "Failed to delete expense claim.")
		return
	}
	auditExpDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Expense claim deleted."})
}

// Transition POST /api/tenant/expenses/{uuid}/transition  body {"toStatusCode":"..."}
func (h *ExpenseOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authExpByUUID(w, r, uuid, authz.ActionTransition)
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
	exp, err := expense.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		expFail(w, err, "Failed to apply transition.")
		return
	}
	auditExp(r, pool, identityID, "transition", uuid, nil, exp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "expense": exp})
}

// Approve POST /api/tenant/expenses/{uuid}/approve
func (h *ExpenseOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authExpByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		expFail(w, err, "Failed to approve expense claim.")
		return
	}
	exp, err := expense.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		if errors.Is(err, expense.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		expFail(w, err, "Failed to approve expense claim.")
		return
	}
	auditExp(r, pool, identityID, "approve", uuid, nil, exp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "expense": exp})
}

// Reject POST /api/tenant/expenses/{uuid}/reject  body {"reason":"..."}
func (h *ExpenseOps) Reject(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authExpByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	exp, err := expense.Reject(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), req.Reason)
	if err != nil {
		if errors.Is(err, expense.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		expFail(w, err, "Failed to reject expense claim.")
		return
	}
	auditExp(r, pool, identityID, "reject", uuid, nil, exp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "expense": exp})
}
