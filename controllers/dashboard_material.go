package controllers

import (
	"net/http"
	"sort"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
)

// materialConsumptionLimit is how many rows the Material consumption
// widget's ranked list shows.
const materialConsumptionLimit = 5

// materialRowOut is one row in the Material consumption widget's JSON
// response. NetUsed is what actually left stock net of remnants recovered —
// the number the widget leads with (see netUsedArea) — while ConsumedArea/
// RecoveredArea/ScrappedArea are the raw components, kept in the payload so
// the frontend's sub-label can say "8 slabs · 46 recovered" without a second
// round trip. Formatting (units, rounding) stays client-side, same as every
// other widget.
type materialRowOut struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	UnitCode      string  `json:"unitCode"`
	ColorHex      string  `json:"colorHex"`
	NetUsed       float64 `json:"netUsed"`
	ConsumedArea  float64 `json:"consumedArea"`
	RecoveredArea float64 `json:"recoveredArea"`
	ScrappedArea  float64 `json:"scrappedArea"`
	SlabCount     int     `json:"slabCount"`
}

// netUsedArea is the stone genuinely gone: area that left stock as 'consumed'
// minus what came back as usable 'recovered' remnants. Clamped at zero rather
// than allowed to go negative -- a window boundary can catch a recovery whose
// matching consumption happened just before the window started (e.g. a slab
// cut yesterday, its remnant logged today), and "used negative material" is
// not a meaningful reading to show, only a real one nudged to zero.
func netUsedArea(consumed, recovered float64) float64 {
	net := consumed - recovered
	if net < 0 {
		return 0
	}
	return net
}

// buildMaterialConsumption resolves the Material consumption widget's data
// given two injectable dependencies -- check (the caller's authz decision)
// and fetch (every item with ledger activity in the window) -- so the
// net/rank/truncate logic is testable without a real tenant pool (see
// dashboard_material_test.go).
//
// A row whose net usage is zero (recovered fully offset consumed, or the
// item's only activity was a scrap with nothing consumed) is dropped: it
// isn't "material consumption" from the shop's point of view, and letting it
// occupy one of the five ranked slots would push out a row that mattered.
//
// materialCount/slabTotal are computed over every row BEFORE truncation --
// same "+N more" convention as every other capped-list widget this session
// (recentRecordsLimit, salesOrdersAtRiskLimit, topCustomersLimit,
// inventoryAlertsLimit).
//
// ok=false when the caller holds no inventory_item:read grant; the handler
// maps that to 403.
func buildMaterialConsumption(
	check func() (authz.Decision, error),
	fetch func() ([]inventory.ConsumptionRow, error),
	limit int,
) (rows []materialRowOut, materialCount int, slabTotal int, ok bool, err error) {
	decision, err := check()
	if err != nil {
		return nil, 0, 0, false, err
	}
	if !decision.Allowed {
		return nil, 0, 0, false, nil
	}

	candidates, err := fetch()
	if err != nil {
		return nil, 0, 0, false, err
	}

	out := make([]materialRowOut, 0, len(candidates))
	for _, c := range candidates {
		net := netUsedArea(c.ConsumedArea, c.RecoveredArea)
		if net <= 0 {
			continue
		}
		out = append(out, materialRowOut{
			ID: c.ItemID, Name: c.ItemName, UnitCode: c.UnitCode, ColorHex: c.ColorHex,
			NetUsed: net, ConsumedArea: c.ConsumedArea, RecoveredArea: c.RecoveredArea,
			ScrappedArea: c.ScrappedArea, SlabCount: c.SlabCount,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].NetUsed > out[j].NetUsed })

	materialCount = len(out)
	for _, r := range out {
		slabTotal += r.SlabCount
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, materialCount, slabTotal, true, nil
}

// MaterialConsumption serves the Material consumption widget's data: the
// tenant's most-used slab-tracked materials by net area consumed (consumed
// minus recovered remnants) within the console's selected range, ranked
// highest-first. No dashboard_widget permission gate -- gated entirely by
// the caller's own inventory_item:read grant, same as InventoryAlerts.
// GET /api/tenant/dashboard/widgets/material-consumption/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) MaterialConsumption(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	rawRange := r.URL.Query().Get("range")
	since, ok := parseDashboardRange(rawRange, time.Now())
	if !ok {
		fail(w, http.StatusBadRequest, "range must be one of all, 7d, 30d, quarter.")
		return
	}
	_, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	ctx := r.Context()

	rows, materialCount, slabTotal, ok, err := buildMaterialConsumption(
		func() (authz.Decision, error) {
			return authz.Check(ctx, pool, payload.ID, authz.ResourceInventoryItem, authz.ActionRead)
		},
		func() ([]inventory.ConsumptionRow, error) {
			return inventory.MaterialConsumptionRows(ctx, pool, since)
		},
		materialConsumptionLimit,
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load material consumption.")
		return
	}
	if !ok {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceInventoryItem), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "You do not have permission to read inventory items.")
		return
	}

	rangeLabel := rawRange
	if rangeLabel == "" {
		rangeLabel = "all"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":       true,
		"range":         rangeLabel,
		"materials":     rows,
		"materialCount": materialCount,
		"slabTotal":     slabTotal,
	})
}
