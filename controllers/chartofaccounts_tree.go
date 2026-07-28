package controllers

import (
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
	"stonesuite-backend/query"
)

// Tree GET /api/tenant/finance/accounts/tree — the reporting screen.
// Structure only: no balances, because there is no general ledger yet.
func (h *ChartOfAccountsOps) Tree(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}

	cats, subs, err := chartofaccounts.Categories(r.Context(), pool)
	if err != nil {
		coaFail(w, err, "Failed to load account categories.")
		return
	}

	// The tree is the whole chart, not a page of it: 127 seeded accounts plus
	// user additions is small enough to assemble in one pass, and a paginated
	// report tree would be meaningless.
	page, err := chartofaccounts.Search(r.Context(), pool,
		query.Request{Limit: query.MaxLimit}, chartofaccounts.Filters{})
	if err != nil {
		coaFail(w, err, "Failed to load accounts.")
		return
	}
	all := page.Records
	for page.HasMore {
		page, err = chartofaccounts.Search(r.Context(), pool,
			query.Request{Limit: query.MaxLimit, Cursor: page.NextCursor},
			chartofaccounts.Filters{})
		if err != nil {
			coaFail(w, err, "Failed to load accounts.")
			return
		}
		all = append(all, page.Records...)
	}

	opts := chartofaccounts.TreeOptions{}
	if v, err := strconv.ParseBool(r.URL.Query().Get("includeInactive")); err == nil {
		opts.IncludeInactive = v
	}
	if v, err := strconv.ParseBool(r.URL.Query().Get("includeHidden")); err == nil {
		opts.IncludeHidden = v
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"sections": chartofaccounts.BuildTree(cats, subs, all, opts),
	})
}

// Categories GET /api/tenant/finance/accounts/categories — the fixed
// 9-category / 17-sub-category reference tree. Read-only (AD-1).
func (h *ChartOfAccountsOps) Categories(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	cats, subs, err := chartofaccounts.Categories(r.Context(), pool)
	if err != nil {
		coaFail(w, err, "Failed to load account categories.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "categories": cats, "subCategories": subs,
	})
}
