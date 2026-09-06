package controllers

import (
	"net/http"
	"sort"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
	"stonesuite-backend/middleware"
)

// inventoryAlertsLimit is how many rows the Inventory alerts widget's list
// shows.
const inventoryAlertsLimit = 5

// severityRank orders the three alert tiers most-urgent-first: a stock
// commitment already broken (short) outranks simply having none (out),
// which outranks a configured threshold being crossed (low). Used only to
// sort -- the tier string itself is what's sent to the frontend.
var severityRank = map[string]int{"short": 0, "out": 1, "low": 2}

// stockAlertOut is one row in the Inventory alerts widget's JSON response.
type stockAlertOut struct {
	ID           string  `json:"id"`
	ItemName     string  `json:"itemName"`
	Warehouse    string  `json:"warehouse"`
	OnHand       float64 `json:"onHand"`
	Allocated    float64 `json:"allocated"`
	ReorderPoint float64 `json:"reorderPoint"`
	Severity     string  `json:"severity"` // short | out | low
}

// classifyStockAlert reports whether a stock row is actually an alert and,
// if so, its severity tier and gap -- how far below where it needs to be,
// used to rank rows within the same tier (biggest gap first). A row that
// isn't a problem at all returns ok=false.
//
// Tier precedence matters: a broken commitment (allocated > on-hand) is
// "short" even when on-hand happens to also be at or under a configured
// reorder point -- the active shortfall is the more urgent fact. Only once
// that's ruled out does zero-on-hand ("out") get checked, and only once
// that's ruled out does the reorder point ("low") get checked -- so a
// reorder point is never consulted at all unless the item has both stock
// and no open shortfall against it.
func classifyStockAlert(onHand, allocated, reorderPoint float64) (severity string, gap float64, ok bool) {
	switch {
	case allocated > onHand:
		return "short", allocated - onHand, true
	case onHand <= 0:
		return "out", reorderPoint, true
	case reorderPoint > 0 && onHand <= reorderPoint:
		return "low", reorderPoint - onHand, true
	default:
		return "", 0, false
	}
}

// buildInventoryAlerts resolves the Inventory alerts widget's data given two
// injectable dependencies -- check (the caller's authz decision) and fetch
// (every candidate stock row) -- so the classify/rank/truncate logic is
// testable without a real tenant pool (see dashboard_inventory_test.go).
// fetch is expected to already be narrowed to candidate rows (see
// inventory.StockAlertCandidates), but classifyStockAlert is applied to
// every row regardless -- a healthy row slipping through fetch is still
// correctly excluded rather than trusted blindly.
//
// alertCount is the full candidate count, not truncated to limit -- the
// widget's "+N more alerts" hint needs the true total, same convention as
// every other capped-list widget this session (recentRecordsLimit,
// salesOrdersAtRiskLimit, topCustomersLimit).
//
// ok=false when the caller holds no inventory_item:read grant; the handler
// maps that to 403.
func buildInventoryAlerts(
	check func() (authz.Decision, error),
	fetch func() ([]inventory.StockLevel, error),
	limit int,
) (alerts []stockAlertOut, alertCount int, ok bool, err error) {
	decision, err := check()
	if err != nil {
		return nil, 0, false, err
	}
	if !decision.Allowed {
		return nil, 0, false, nil
	}

	levels, err := fetch()
	if err != nil {
		return nil, 0, false, err
	}

	type candidate struct {
		out  stockAlertOut
		rank int
		gap  float64
	}
	candidates := make([]candidate, 0, len(levels))
	for _, lv := range levels {
		severity, gap, isAlert := classifyStockAlert(lv.OnHand, lv.Allocated, lv.ReorderPoint)
		if !isAlert {
			continue
		}
		candidates = append(candidates, candidate{
			out: stockAlertOut{
				ID: lv.ItemID, ItemName: lv.ItemName, Warehouse: lv.WarehouseName,
				OnHand: lv.OnHand, Allocated: lv.Allocated, ReorderPoint: lv.ReorderPoint,
				Severity: severity,
			},
			rank: severityRank[severity],
			gap:  gap,
		})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].gap > candidates[j].gap
	})

	alertCount = len(candidates)
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	alerts = make([]stockAlertOut, len(candidates))
	for i, c := range candidates {
		alerts[i] = c.out
	}
	return alerts, alertCount, true, nil
}

// InventoryAlerts serves the Inventory alerts widget's data: the most
// urgent stock problems tenant-wide -- open sales-order commitments
// exceeding on-hand stock ("short"), zero on-hand ("out"), and on-hand at
// or under a configured reorder point ("low") -- ranked most-severe-tier
// first, then by size of the gap. No dashboard_widget permission gate --
// gated entirely by the caller's own inventory_item:read grant.
//
// Stock levels are current-state, not date-windowed data -- range is
// accepted and validated for contract uniformity with the other widgets
// (an unrecognized value still 400s, and the resolved value is echoed
// back), but it never changes what's queried.
// GET /api/tenant/dashboard/widgets/inventory-alerts/data?range=all|7d|30d|quarter
func (h *DashboardUIOps) InventoryAlerts(w http.ResponseWriter, r *http.Request) {
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
	if _, ok := parseDashboardRange(rawRange, time.Now()); !ok {
		fail(w, http.StatusBadRequest, "range must be one of all, 7d, 30d, quarter.")
		return
	}
	_, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	ctx := r.Context()

	alerts, alertCount, ok, err := buildInventoryAlerts(
		func() (authz.Decision, error) {
			return authz.Check(ctx, pool, payload.ID, authz.ResourceInventoryItem, authz.ActionRead)
		},
		func() ([]inventory.StockLevel, error) {
			return inventory.StockAlertCandidates(ctx, pool)
		},
		inventoryAlertsLimit,
	)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load inventory alerts.")
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
		"success":    true,
		"range":      rangeLabel,
		"alerts":     alerts,
		"alertCount": alertCount,
	})
}
