package controllers

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/reports"
	"stonesuite-backend/tenancy"
)

// purchasesReportRequest is a parsed + authorized Purchases report call.
type purchasesReportRequest struct {
	filter reports.PurchaseFilter
	pool   *pgxpool.Pool
}

// parseReportWindow reads the optional from/to date window.
func parseReportWindow(w http.ResponseWriter, r *http.Request) (from, to time.Time, ok bool) {
	q := r.URL.Query()
	from, ok = parseReportDate(q.Get("from"))
	if !ok {
		fail(w, http.StatusBadRequest, "from must be a YYYY-MM-DD date.")
		return from, to, false
	}
	to, ok = parseReportDate(q.Get("to"))
	if !ok {
		fail(w, http.StatusBadRequest, "to must be a YYYY-MM-DD date.")
		return from, to, false
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		fail(w, http.StatusBadRequest, "to must not be before from.")
		return from, to, false
	}
	return from, to, true
}

// authorizePurchasesReport runs the checks every Purchases report shares: GET
// only, signed in, report_purchases:read. withWindow also parses from/to.
func (h *ReportsOps) authorizePurchasesReport(w http.ResponseWriter, r *http.Request, withWindow bool) (purchasesReportRequest, bool) {
	var req purchasesReportRequest
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return req, false
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return req, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return req, false
	}
	var from, to time.Time
	if withWindow {
		var ok bool
		if from, to, ok = parseReportWindow(w, r); !ok {
			return req, false
		}
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceReportPurchases, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return req, false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to view purchases reports.")
		return req, false
	}
	return purchasesReportRequest{
		filter: reports.PurchaseFilter{From: from, To: to, Scope: decision.Scope, ActorIdentityID: payload.ID},
		pool:   pool,
	}, true
}

// PurchasesByVendor serves Reports > Purchases > Purchases by Vendor.
// GET /api/tenant/reports/purchases/by-vendor?from=&to=
func (h *ReportsOps) PurchasesByVendor(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizePurchasesReport(w, r, true)
	if !ok {
		return
	}
	rows, totals, err := reports.PurchasesByVendor(r.Context(), req.pool, req.filter)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

// PurchasesByItem serves Reports > Purchases > Purchases by Item.
// GET /api/tenant/reports/purchases/by-item?from=&to=
func (h *ReportsOps) PurchasesByItem(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizePurchasesReport(w, r, true)
	if !ok {
		return
	}
	rows, totals, err := reports.PurchasesByItem(r.Context(), req.pool, req.filter)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

// OpenPurchaseOrders serves Reports > Purchases > Open POs & Receiving: the
// purchase orders still waiting on goods, as of today (no date window).
// GET /api/tenant/reports/purchases/open-pos
func (h *ReportsOps) OpenPurchaseOrders(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizePurchasesReport(w, r, false)
	if !ok {
		return
	}
	rows, totals, err := reports.OpenPurchaseOrders(r.Context(), req.pool, req.filter.Scope, req.filter.ActorIdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

// APAging serves Reports > Purchases > AP Aging: unpaid vendor bills by how
// far past due they are, as of today (no date window).
// GET /api/tenant/reports/purchases/ap-aging
func (h *ReportsOps) APAging(w http.ResponseWriter, r *http.Request) {
	req, ok := h.authorizePurchasesReport(w, r, false)
	if !ok {
		return
	}
	rows, totals, err := reports.APAging(r.Context(), req.pool, req.filter.Scope, req.filter.ActorIdentityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "labels": reports.AgingBucketLabels, "totals": totals, "rows": rows,
	})
}
