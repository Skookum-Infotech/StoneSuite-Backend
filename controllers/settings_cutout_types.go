package controllers

import (
	"net/http"

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

type cutoutType struct {
	Code  string `json:"code"`
	Label string `json:"label"`
}

// GetCutoutTypes GET /api/tenant/settings/cutout-types — the kinds of opening
// (sink, cooktop, faucet, fireplace, bathtub, other) the drawing editor's
// Cutout dialog offers. Read-only reference data seeded into every tenant, so
// like edge codes any authenticated tenant user may read it, with no specific
// RBAC resource.
func GetCutoutTypes(w http.ResponseWriter, r *http.Request) {
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
		SELECT cutout_type_code, cutout_type_label FROM lkp_cutout_type
		WHERE cutout_type_is_active ORDER BY cutout_type_sort_order, cutout_type_code`)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load cutout types.")
		return
	}
	defer rows.Close()

	types := []cutoutType{}
	for rows.Next() {
		var t cutoutType
		if err := rows.Scan(&t.Code, &t.Label); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load cutout types.")
			return
		}
		types = append(types, t)
	}
	if err := rows.Err(); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load cutout types.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cutoutTypes": types})
}
