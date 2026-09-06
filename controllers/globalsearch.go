package controllers

import (
	"fmt"
	"net/http"
	"strings"

	"stonesuite-backend/globalsearch"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// minSearchTermLen avoids pathological full-table ILIKE scans on 1-char terms.
const minSearchTermLen = 2

// GlobalSearchOps handles the cross-module search endpoint.
type GlobalSearchOps struct{}

// NewGlobalSearchOps constructs the handler group.
func NewGlobalSearchOps() *GlobalSearchOps { return &GlobalSearchOps{} }

// Search GET /api/tenant/search?q=term&modules=quote,invoice fans the term out
// to every module the caller has read access to (see globalsearch.Search) and
// returns results grouped by entity type. There is no single RBAC resource
// for this endpoint by design -- each group is independently gated inside the
// fan-out, exactly as if the caller had used that module's own search box.
func (h *GlobalSearchOps) Search(w http.ResponseWriter, r *http.Request) {
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

	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) < minSearchTermLen {
		fail(w, http.StatusBadRequest, fmt.Sprintf("Search term must be at least %d characters.", minSearchTermLen))
		return
	}
	var modules []string
	if m := r.URL.Query().Get("modules"); m != "" {
		modules = strings.Split(m, ",")
	}

	resp := globalsearch.Search(r.Context(), pool, payload.ID, q, modules)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"query":   resp.Query,
		"groups":  resp.Groups,
	})
}
