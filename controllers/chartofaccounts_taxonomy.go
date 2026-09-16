package controllers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
)

// Category/sub-category maintenance. These endpoints change the shape of the
// chart itself rather than the accounts inside it, so they are gated on
// chart_of_account:configure ("edit definitions/settings") rather than
// :create/:update -- the same grant account-defaults uses, and for the same
// reason: repointing what the chart means is a different privilege from adding
// a row to it.
//
// Routes (all under /api/tenant/finance):
//
//	POST  /accounts/categories                  — append a category
//	PATCH /accounts/categories/{id}             — rename a category
//	POST  /accounts/subcategories               — append a sub-category
//	PATCH /accounts/subcategories/{id}          — rename a sub-category

// CreateCategory POST /api/tenant/finance/accounts/categories
func (h *ChartOfAccountsOps) CreateCategory(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var in chartofaccounts.CategoryCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	cat, err := chartofaccounts.CreateCategory(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to create category.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "category": cat})
}

// RenameCategory PATCH /api/tenant/finance/accounts/categories/{id}
func (h *ChartOfAccountsOps) RenameCategory(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	id, ok := taxonomyID(w, r)
	if !ok {
		return
	}
	var in chartofaccounts.RenameInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	cat, err := chartofaccounts.RenameCategory(r.Context(), pool, id, in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to rename category.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "category": cat})
}

// CreateSubCategory POST /api/tenant/finance/accounts/subcategories
func (h *ChartOfAccountsOps) CreateSubCategory(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var in chartofaccounts.SubCategoryCreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	sub, err := chartofaccounts.CreateSubCategory(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to create sub-category.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "subCategory": sub})
}

// RenameSubCategory PATCH /api/tenant/finance/accounts/subcategories/{id}
func (h *ChartOfAccountsOps) RenameSubCategory(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	id, ok := taxonomyID(w, r)
	if !ok {
		return
	}
	var in chartofaccounts.RenameInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	sub, err := chartofaccounts.RenameSubCategory(r.Context(), pool, id, in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to rename sub-category.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "subCategory": sub})
}

// taxonomyID reads the {id} path value as a positive integer, writing a 400 and
// reporting false when it is not one. Categories and sub-categories are keyed by
// SERIAL, not uuid, so there is no uuid guard to lean on here.
func taxonomyID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		fail(w, http.StatusBadRequest, "Invalid id.")
		return 0, false
	}
	return id, true
}
