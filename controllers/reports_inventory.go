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

// authorizeInventoryReport runs the checks every Inventory report shares: GET
// only, signed in, report_inventory:read. withWindow also parses from/to.
// Stock has no per-row owner, so there is no data scope to carry forward.
func (h *ReportsOps) authorizeInventoryReport(w http.ResponseWriter, r *http.Request, withWindow bool) (*pgxpool.Pool, time.Time, time.Time, bool) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return nil, time.Time{}, time.Time{}, false
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, time.Time{}, time.Time{}, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, time.Time{}, time.Time{}, false
	}
	var from, to time.Time
	if withWindow {
		var ok bool
		if from, to, ok = parseReportWindow(w, r); !ok {
			return nil, time.Time{}, time.Time{}, false
		}
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceReportInventory, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, time.Time{}, time.Time{}, false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to view inventory reports.")
		return nil, time.Time{}, time.Time{}, false
	}
	return pool, from, to, true
}

// StockOnHand serves Reports > Inventory > Stock on Hand & Valuation, as of today.
// GET /api/tenant/reports/inventory/stock
func (h *ReportsOps) StockOnHand(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorizeInventoryReport(w, r, false)
	if !ok {
		return
	}
	rows, totals, err := reports.StockOnHand(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

// SlabAvailability serves Reports > Inventory > Slab Availability, as of today.
// GET /api/tenant/reports/inventory/slabs
func (h *ReportsOps) SlabAvailability(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorizeInventoryReport(w, r, false)
	if !ok {
		return
	}
	rows, totals, err := reports.SlabAvailability(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "statuses": reports.SlabStatuses, "labels": reports.SlabStatusLabels, "totals": totals, "rows": rows,
	})
}

// LowStock serves Reports > Inventory > Low Stock, as of today.
// GET /api/tenant/reports/inventory/low-stock
func (h *ReportsOps) LowStock(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorizeInventoryReport(w, r, false)
	if !ok {
		return
	}
	rows, totals, err := reports.LowStock(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}

// StockMovement serves Reports > Inventory > Stock Movement for a date window.
// GET /api/tenant/reports/inventory/movement?from=&to=
func (h *ReportsOps) StockMovement(w http.ResponseWriter, r *http.Request) {
	pool, from, to, ok := h.authorizeInventoryReport(w, r, true)
	if !ok {
		return
	}
	rows, totals, err := reports.StockMovement(r.Context(), pool, from, to)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Could not build the report.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "totals": totals, "rows": rows})
}
