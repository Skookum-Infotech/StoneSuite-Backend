package controllers

import (
	"errors"
	"net/http"
	"time"

	"stonesuite-backend/tenancy"
	"stonesuite-backend/usage"
)

// usageDay is the width of one usage day, for half-open sign-in ranges.
const usageDay = 24 * time.Hour

// usageWindow resolves the window end (latest rolled-up day, else yesterday UTC).
func (h *PlatformUsageOps) usageWindow(r *http.Request, days int) (usage.Window, error) {
	latest, err := h.store.LatestRolledUpDay(r.Context())
	if err != nil {
		return usage.Window{}, err
	}
	return usage.NewWindow(usage.WindowEnd(latest, h.now()), days), nil
}

func tenantInfo(t tenancy.Tenant) usage.TenantInfo {
	return usage.TenantInfo{ID: t.ID, Slug: t.Slug, DisplayName: t.DisplayName, Status: t.Status}
}

func tenantIDsOf(ts []tenancy.Tenant) []string {
	ids := make([]string, 0, len(ts))
	for _, t := range ts {
		ids = append(ids, t.ID)
	}
	return ids
}

// Overview handles GET /api/platform/usage/overview?period&limit&cursor.
func (h *PlatformUsageOps) Overview(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformAdmin(w, r); !ok {
		return
	}
	period, days, ok := parsePeriodParam(w, r)
	if !ok {
		return
	}
	limit, ok := parseLimitParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	win, err := h.usageWindow(r, days)
	if err != nil {
		h.failLoad(w, r, "latest rolled-up day", err)
		return
	}
	page, next, err := h.cp.ListTenantsPage(ctx, limit, r.URL.Query().Get("cursor"))
	if errors.Is(err, tenancy.ErrInvalidTenantCursor) {
		fail(w, http.StatusBadRequest, "Invalid cursor.")
		return
	}
	if err != nil {
		h.failLoad(w, r, "list tenants page", err)
		return
	}
	all, err := h.cp.ListTenants(ctx)
	if err != nil {
		h.failLoad(w, r, "list tenants", err)
		return
	}
	ids := tenantIDsOf(all)
	from := win.Prev().Start()
	daily, err := h.store.DailyValues(ctx, ids, from, win.End)
	if err != nil {
		h.failLoad(w, r, "daily values", err)
		return
	}
	tokens, err := h.store.TokensByDay(ctx, ids, from, win.End)
	if err != nil {
		h.failLoad(w, r, "token usage", err)
		return
	}
	now := h.now()
	severities, err := h.store.TopSeverityByTenant(ctx, now)
	if err != nil {
		h.failLoad(w, r, "top severity", err)
		return
	}
	idx := usage.NewValueIndex(daily, tokens)
	infos := make([]usage.TenantInfo, 0, len(all))
	for _, t := range all {
		infos = append(infos, tenantInfo(t))
	}
	rows := make([]usage.TenantUsageRow, 0, len(page))
	for _, t := range page {
		sign, err := h.store.SignInStats(ctx, t.ID, win.Start(), win.End.Add(usageDay))
		if err != nil {
			h.failLoad(w, r, "sign-in stats", err)
			return
		}
		rows = append(rows, usage.BuildRow(tenantInfo(t), idx, win, sign, severities[t.ID]))
	}
	writeJSON(w, http.StatusOK, struct {
		Success     bool                   `json:"success"`
		GeneratedAt time.Time              `json:"generatedAt"`
		Period      string                 `json:"period"`
		Fleet       usage.FleetTotals      `json:"fleet"`
		Tenants     []usage.TenantUsageRow `json:"tenants"`
		NextCursor  string                 `json:"nextCursor,omitempty"`
	}{true, now.UTC(), period, usage.BuildFleet(infos, idx, win, tenancy.StatusActive), rows, next})
}

// TenantDetail handles GET /api/platform/usage/tenants/{tenantId}?period.
func (h *PlatformUsageOps) TenantDetail(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformAdmin(w, r); !ok {
		return
	}
	period, days, ok := parsePeriodParam(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	tenantID := r.PathValue("tenantId")
	if !uuidPattern.MatchString(tenantID) {
		fail(w, http.StatusNotFound, msgUsageUnavailable)
		return
	}
	t, err := h.cp.TenantByID(ctx, tenantID)
	if errors.Is(err, tenancy.ErrTenantNotFound) || (err == nil && t == nil) {
		fail(w, http.StatusNotFound, msgUsageUnavailable)
		return
	}
	if err != nil {
		h.failLoad(w, r, "load tenant", err)
		return
	}
	win, err := h.usageWindow(r, days)
	if err != nil {
		h.failLoad(w, r, "latest rolled-up day", err)
		return
	}
	in, err := h.detailInput(r, *t, period, win)
	if err != nil {
		h.failLoad(w, r, "tenant detail", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool `json:"success"`
		usage.TenantDetail
	}{true, usage.BuildDetail(in)})
}

// detailInput gathers every store read the tenant detail needs.
func (h *PlatformUsageOps) detailInput(r *http.Request, t tenancy.Tenant, period string, win usage.Window) (usage.DetailInput, error) {
	ctx, now := r.Context(), h.now()
	one := []string{t.ID}
	from := win.Prev().Start()
	if dauFrom := win.End.AddDate(0, 0, -(usageDaysPerMonth - 1)); dauFrom.Before(from) {
		from = dauFrom
	}
	daily, err := h.store.DailyValues(ctx, one, from, win.End)
	if err != nil {
		return usage.DetailInput{}, err
	}
	tokens, err := h.store.TokensByDay(ctx, one, from, win.End)
	if err != nil {
		return usage.DetailInput{}, err
	}
	all, err := h.cp.ListTenants(ctx)
	if err != nil {
		return usage.DetailInput{}, err
	}
	allIDs := tenantIDsOf(all)
	fleetDaily, err := h.store.DailyValues(ctx, allIDs, win.Start(), win.End)
	if err != nil {
		return usage.DetailInput{}, err
	}
	sign, err := h.store.SignInStats(ctx, t.ID, win.Start(), win.End.Add(usageDay))
	if err != nil {
		return usage.DetailInput{}, err
	}
	sign30, err := h.store.SignInStats(ctx, t.ID, win.End.AddDate(0, 0, -(usageDaysPerMonth-1)), win.End.Add(usageDay))
	if err != nil {
		return usage.DetailInput{}, err
	}
	bySource, err := h.store.TokensBySource(ctx, t.ID, win.Start(), win.End)
	if err != nil {
		return usage.DetailInput{}, err
	}
	tables, measured, err := h.store.TableSizes(ctx, t.ID)
	if err != nil {
		return usage.DetailInput{}, err
	}
	alerts, err := h.store.TenantAlerts(ctx, t.ID, now.AddDate(0, 0, -usage.AlertHistoryDays))
	if err != nil {
		return usage.DetailInput{}, err
	}
	severities, err := h.store.TopSeverityByTenant(ctx, now)
	if err != nil {
		return usage.DetailInput{}, err
	}
	return usage.DetailInput{
		Now: now, Period: period, Info: tenantInfo(t), Window: win,
		Values: usage.NewValueIndex(daily, tokens), Sign: sign, Sign30: sign30,
		TopSev:  severities[t.ID],
		FleetDB: usage.SumLastAcross(usage.NewValueIndex(fleetDaily), allIDs, usage.MetricDBBytes, win.Start(), win.End),
		Tables:  tables, TablesAt: measured, Tokens: bySource, Alerts: alerts,
	}, nil
}
