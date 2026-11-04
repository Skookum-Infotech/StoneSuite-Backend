package controllers

import (
	"net/http"

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

type edgeCode struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// GetEdgeCodes GET /api/tenant/settings/edge-codes — the countertop edge
// profile options the drawing editor's inspector offers per piece edge
// (build spec §6). Read-only reference data: any authenticated tenant user
// may read it, no specific RBAC resource — the same tier inventory lookups'
// own reads use for "a bin clerk who may not touch the catalogue still
// needs the vocabularies."
func GetEdgeCodes(w http.ResponseWriter, r *http.Request) {
	if _, err := middleware.GetUserFromContext(r.Context()); err != nil {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT edge_code_code, edge_code_label FROM lkp_edge_code
		WHERE edge_code_is_active ORDER BY edge_code_code`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load edge codes.")
		return
	}
	defer rows.Close()

	var codes []edgeCode
	for rows.Next() {
		var c edgeCode
		if err := rows.Scan(&c.Code, &c.Label); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load edge codes.")
			return
		}
		codes = append(codes, c)
	}
	if err := rows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load edge codes.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "edgeCodes": codes})
}
