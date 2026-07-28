package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

// ChartOfAccountsOps handles the Finance chart-of-accounts endpoints.
//
// Like InventoryOps, the chart of accounts is shared tenant-global reference
// data rather than an owner-scoped CRM record, so there is no per-record IDOR
// scope check beyond the resource-level chart_of_account:<action> permission.
// This is deliberate, not an omission: coa_account has no owner column to
// scope against.
//
// Routes (all under /api/tenant/finance):
//
//	GET    /accounts                    — list (cursor-paginated, ?postable=&active=&visible=)
//	POST   /accounts/search             — filter + sort + search + pagination
//	GET    /accounts/tree               — grouped report
//	GET    /accounts/categories         — the fixed reference tree
//	POST   /accounts                    — create
//	GET    /accounts/{uuid}             — get
//	PATCH  /accounts/{uuid}             — update
//	DELETE /accounts/{uuid}             — soft delete
//	PATCH  /accounts/bulk               — bulk activate/hide
//	GET    /accounts/{uuid}/history     — audit trail
//	GET    /account-defaults            — mapping slots
//	PATCH  /account-defaults/{slotKey}  — repoint a slot
type ChartOfAccountsOps struct {
	cipher *secret.Cipher // nil when SECRET_ENCRYPTION_KEY is unset; writes fail closed
}

// NewChartOfAccountsOps constructs the handler group. cipher may be nil in
// local dev; bank-account writes then fail with 503 rather than storing an
// account number in plaintext, mirroring NewSSOOps.
func NewChartOfAccountsOps(cipher *secret.Cipher) *ChartOfAccountsOps {
	return &ChartOfAccountsOps{cipher: cipher}
}

// authCOA resolves JWT + tenant pool + the chart_of_account:<action> grant.
func (h *ChartOfAccountsOps) authCOA(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceChartOfAccount, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"resource", string(authz.ResourceChartOfAccount), "action", string(action))
		fail(w, http.StatusForbidden,
			"You do not have permission to "+string(action)+" chart of accounts entries.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// coaFail maps a store error to an HTTP response.
func coaFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, chartofaccounts.ErrNotFound):
		fail(w, http.StatusNotFound, "Account not found.")
	case errors.Is(err, chartofaccounts.ErrCipherUnavailable):
		fail(w, http.StatusServiceUnavailable,
			"Bank account details cannot be saved: secret encryption is not configured.")
	case chartofaccounts.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		if conflict, ok := chartofaccounts.IsConflict(err); ok {
			writeJSON(w, http.StatusConflict, map[string]any{
				"success":       false,
				"message":       conflict.Msg,
				"blockingSlots": conflict.BlockingSlots,
			})
			return
		}
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// boolParam reads an optional true/false query parameter.
func boolParam(r *http.Request, key string) *bool {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &v
}

// filtersFrom builds the query-param toggles. The dropdown call every
// transaction screen makes is ?postable=true&active=true.
func filtersFrom(r *http.Request) chartofaccounts.Filters {
	f := chartofaccounts.Filters{
		Postable: boolParam(r, "postable"),
		Active:   boolParam(r, "active"),
		Visible:  boolParam(r, "visible"),
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("subCategoryId")); err == nil && n > 0 {
		f.SubCategoryID = &n
	}
	return f
}

// List GET /api/tenant/finance/accounts
func (h *ChartOfAccountsOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	page, err := chartofaccounts.Search(r.Context(), pool, req, filtersFrom(r))
	if err != nil {
		coaFail(w, err, "Failed to list accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Search POST /api/tenant/finance/accounts/search
func (h *ChartOfAccountsOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
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
	page, err := chartofaccounts.Search(r.Context(), pool, req, filtersFrom(r))
	if err != nil {
		coaFail(w, err, "Failed to search accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

// Create POST /api/tenant/finance/accounts
func (h *ChartOfAccountsOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in chartofaccounts.CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	acct, err := chartofaccounts.Create(r.Context(), pool, h.cipher, in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to create account.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "account": acct})
}

// Get GET /api/tenant/finance/accounts/{uuid}
func (h *ChartOfAccountsOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	acct, err := chartofaccounts.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		coaFail(w, err, "Failed to load account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "account": acct})
}

// Update PATCH /api/tenant/finance/accounts/{uuid}
func (h *ChartOfAccountsOps) Update(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in chartofaccounts.UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	acct, err := chartofaccounts.Update(r.Context(), pool, h.cipher,
		r.PathValue("uuid"), in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to update account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "account": acct})
}

// Delete DELETE /api/tenant/finance/accounts/{uuid}
func (h *ChartOfAccountsOps) Delete(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := chartofaccounts.SoftDelete(r.Context(), pool,
		r.PathValue("uuid"), resolveEmployeeID(r, identityID)); err != nil {
		coaFail(w, err, "Failed to delete account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Account deleted."})
}
