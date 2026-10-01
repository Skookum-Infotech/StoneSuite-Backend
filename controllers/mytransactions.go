package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/mytransactions"
	"stonesuite-backend/tenancy"
)

// myTransactionsResource names this endpoint in security-event logs. It is not
// an RBAC resource: each record type is gated by its own inside the package.
const myTransactionsResource = "my-transactions"

// MyTransactionsOps serves the "My Transactions" page: the records the signed-in
// user created or last updated, across every module they may read.
type MyTransactionsOps struct{}

// NewMyTransactionsOps constructs the handler group.
func NewMyTransactionsOps() *MyTransactionsOps { return &MyTransactionsOps{} }

// writeMyTransactionsError maps a package error to its HTTP response and
// reports whether it handled one. Like global search and the dashboard's recent
// records, there is no single RBAC resource: a caller who may read no record
// type at all is a permission denial.
func writeMyTransactionsError(w http.ResponseWriter, r *http.Request, identityID string, err error) bool {
	var invalid *mytransactions.InvalidParamError
	switch {
	case err == nil:
		return false
	case errors.As(err, &invalid):
		fail(w, http.StatusBadRequest, "Invalid "+invalid.Param+": "+invalid.Msg+".")
	case errors.Is(err, mytransactions.ErrNoAccess):
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", myTransactionsResource, "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read any record type.")
	default:
		slog.Error("my transactions: request failed", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load your transactions.")
	}
	return true
}

// List GET /api/tenant/my-transactions
//
// Query params (all optional): role (all|created|updated), type (a record-type
// key, e.g. sales_order), q (contains-match on number/name/account), page
// (1-based), limit (1..100). The response carries the page's rows and the
// total number of matching rows, so the client can render page numbers.
func (h *MyTransactionsOps) List(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	q := r.URL.Query()
	params := mytransactions.Params{
		IdentityID: payload.ID,
		Role:       q.Get("role"),
		Type:       q.Get("type"),
		Search:     q.Get("q"),
	}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"page", &params.Page}, {"limit", &params.Limit}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			fail(w, http.StatusBadRequest, p.name+" must be a non-negative integer.")
			return
		}
		*p.dst = n
	}

	page, err := mytransactions.List(r.Context(), pool, params)
	if writeMyTransactionsError(w, r, payload.ID, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"rows":    page.Rows,
		"total":   page.Total,
		"page":    page.Page,
		"limit":   page.Limit,
	})
}

// Overview GET /api/tenant/my-transactions/summary
//
// The unfiltered stat-card counts and the record types the caller may read.
// Separate from List so turning a page or changing a filter does not recount
// everything.
func (h *MyTransactionsOps) Overview(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	overview, err := mytransactions.GetOverview(r.Context(), pool, payload.ID)
	if writeMyTransactionsError(w, r, payload.ID, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"summary": overview.Summary,
		"types":   overview.Types,
	})
}
