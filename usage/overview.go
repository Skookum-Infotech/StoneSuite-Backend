package usage

import (
	"time"
)

// Supported period selectors for the platform usage API.
const (
	PeriodDefault = "30d"
	period7d      = "7d"
	period90d     = "90d"

	periodDays7  = 7
	periodDays30 = 30
	periodDays90 = 90

	// dauMauDays is the look-back for the DAU/MAU ratio.
	dauMauDays = 30

	dateLayout    = "2006-01-02"
	percentFactor = 100.0
)

// ParsePeriod converts a 7d|30d|90d selector to its length in days.
func ParsePeriod(s string) (int, bool) {
	switch s {
	case period7d:
		return periodDays7, true
	case PeriodDefault:
		return periodDays30, true
	case period90d:
		return periodDays90, true
	}
	return 0, false
}

// Window is the run of Days complete UTC days ending at End (inclusive).
type Window struct {
	End  time.Time
	Days int
}

// WindowEnd returns the window end day: the latest rolled-up day, else yesterday (UTC).
func WindowEnd(latest *time.Time, now time.Time) time.Time {
	if latest != nil {
		return midnightUTC(*latest)
	}
	return midnightUTC(now).AddDate(0, 0, -1)
}

// NewWindow builds the window of days days ending at end.
func NewWindow(end time.Time, days int) Window {
	return Window{End: midnightUTC(end), Days: days}
}

// Start is the first day of the window.
func (w Window) Start() time.Time { return w.End.AddDate(0, 0, -(w.Days - 1)) }

// Prev is the window of equal length immediately before this one.
func (w Window) Prev() Window {
	return Window{End: w.Start().AddDate(0, 0, -1), Days: w.Days}
}

// Dates lists every day of the window, oldest first.
func (w Window) Dates() []time.Time {
	out := make([]time.Time, 0, w.Days)
	for d := w.Start(); !d.After(w.End); d = d.AddDate(0, 0, 1) {
		out = append(out, d)
	}
	return out
}

// TenantInfo is the control-plane identity of a tenant shown in a usage row.
type TenantInfo struct {
	ID, Slug, DisplayName, Status string
}

// TenantUsageRow is one tenant's row in the overview list (and the detail header).
type TenantUsageRow struct {
	TenantID         string    `json:"tenantId"`
	Slug             string    `json:"slug"`
	DisplayName      string    `json:"displayName"`
	Status           string    `json:"status"`
	ActiveUsers      int       `json:"activeUsers"`
	TotalUsers       int       `json:"totalUsers"`
	ComputeHours     *float64  `json:"computeHours"`
	ComputeHoursPrev *float64  `json:"computeHoursPrev"`
	DBBytes          *float64  `json:"dbBytes"`
	DBBytesPrev      *float64  `json:"dbBytesPrev"`
	R2Bytes          *float64  `json:"r2Bytes"`
	R2BytesPrev      *float64  `json:"r2BytesPrev"`
	AITokens         *float64  `json:"aiTokens"`
	AITokensPrev     *float64  `json:"aiTokensPrev"`
	ErrorRatePct     *float64  `json:"errorRatePct"`
	ErrorRate24hPct  *float64  `json:"errorRate24hPct"`
	ComputeTrend     []float64 `json:"computeTrend"`
	TopSeverity      *string   `json:"topSeverity"`
}

// FleetTotals is the fleet-wide summary shown above the overview list.
type FleetTotals struct {
	ActiveTenants    int       `json:"activeTenants"`
	TotalTenants     int       `json:"totalTenants"`
	ComputeHours     float64   `json:"computeHours"`
	ComputeHoursPrev *float64  `json:"computeHoursPrev"`
	DBBytes          float64   `json:"dbBytes"`
	DBBytesPrev      *float64  `json:"dbBytesPrev"`
	R2Bytes          float64   `json:"r2Bytes"`
	R2BytesPrev      *float64  `json:"r2BytesPrev"`
	AITokens         float64   `json:"aiTokens"`
	AITokensPrev     *float64  `json:"aiTokensPrev"`
	ComputeTrend     []float64 `json:"computeTrend"`
}

// AlertView is the JSON form of a usage alert.
type AlertView struct {
	ID           string     `json:"id"`
	TenantID     string     `json:"tenantId"`
	TenantName   string     `json:"tenantName"`
	RuleKey      string     `json:"ruleKey"`
	MetricKey    string     `json:"metricKey"`
	Severity     string     `json:"severity"`
	Message      string     `json:"message"`
	Detail       string     `json:"detail"`
	FirstSeenAt  time.Time  `json:"firstSeenAt"`
	LastSeenAt   time.Time  `json:"lastSeenAt"`
	Status       string     `json:"status"`
	SnoozedUntil *time.Time `json:"snoozedUntil"`
}

// AlertViews converts alerts to their JSON form; never nil.
func AlertViews(alerts []Alert) []AlertView {
	out := make([]AlertView, 0, len(alerts))
	for _, a := range alerts {
		out = append(out, AlertView{
			ID: a.ID, TenantID: a.TenantID, TenantName: a.TenantName, RuleKey: a.RuleKey,
			MetricKey: a.MetricKey, Severity: a.Severity, Message: a.Message, Detail: a.Detail,
			FirstSeenAt: a.FirstSeenAt, LastSeenAt: a.LastSeenAt, Status: a.Status,
			SnoozedUntil: a.SnoozedUntil,
		})
	}
	return out
}

// nsum accumulates a sum that stays "unmeasured" until a value is added.
type nsum struct {
	v  float64
	ok bool
}

func (n *nsum) add(v float64, ok bool) {
	if ok {
		n.v += v
		n.ok = true
	}
}

func (n nsum) ptr() *float64 {
	if !n.ok {
		return nil
	}
	v := n.v
	return &v
}

// windowTotals is one tenant's headline sums over a window.
type windowTotals struct {
	compute, requests, errors, tokens, db, r2 nsum
}

func totalsFor(x *ValueIndex, tenant string, w Window) windowTotals {
	var t windowTotals
	from, to := w.Start(), w.End
	for _, key := range []string{MetricComputeRequestHours, MetricComputeJobHours} {
		v, ok := x.Sum(tenant, key, from, to)
		t.compute.add(v, ok)
	}
	v, ok := x.Sum(tenant, MetricRequests, from, to)
	t.requests.add(v, ok)
	v, ok = x.Sum(tenant, MetricErrors5xx, from, to)
	t.errors.add(v, ok)
	v, ok = x.Sum(tenant, MetricAITokens, from, to)
	t.tokens.add(v, ok)
	v, ok = x.Last(tenant, MetricDBBytes, from, to)
	t.db.add(v, ok)
	v, ok = x.Last(tenant, MetricR2Bytes, from, to)
	t.r2.add(v, ok)
	return t
}

// errorRatePct is errors/requests as a percentage; nil without a request denominator.
func errorRatePct(errs, reqs nsum) *float64 {
	if !reqs.ok || reqs.v == 0 {
		return nil
	}
	r := errs.v / reqs.v * percentFactor
	return &r
}

// ComputeTrend returns the tenant's daily compute hours (requests + jobs) for each window day, 0-filled.
func ComputeTrend(x *ValueIndex, tenant string, w Window) []float64 {
	dates := w.Dates()
	out := make([]float64, len(dates))
	for i, d := range dates {
		for _, key := range []string{MetricComputeRequestHours, MetricComputeJobHours} {
			if v, ok := x.At(tenant, key, d); ok {
				out[i] += v
			}
		}
	}
	return out
}

// BuildRow assembles one tenant's usage row; topSeverity is "" when it has no watch-list alert.
func BuildRow(info TenantInfo, x *ValueIndex, w Window, sign SignInStats, topSeverity string) TenantUsageRow {
	cur, prev := totalsFor(x, info.ID, w), totalsFor(x, info.ID, w.Prev())
	row := TenantUsageRow{
		TenantID: info.ID, Slug: info.Slug, DisplayName: info.DisplayName, Status: info.Status,
		ActiveUsers: sign.ActiveUsers, TotalUsers: sign.TotalUsers,
		ComputeHours: cur.compute.ptr(), ComputeHoursPrev: prev.compute.ptr(),
		DBBytes: cur.db.ptr(), DBBytesPrev: prev.db.ptr(),
		R2Bytes: cur.r2.ptr(), R2BytesPrev: prev.r2.ptr(),
		AITokens: cur.tokens.ptr(), AITokensPrev: prev.tokens.ptr(),
		ErrorRatePct: errorRatePct(cur.errors, cur.requests),
		ComputeTrend: ComputeTrend(x, info.ID, w),
	}
	var dayErrs, dayReqs nsum
	v, ok := x.At(info.ID, MetricErrors5xx, w.End)
	dayErrs.add(v, ok)
	v, ok = x.At(info.ID, MetricRequests, w.End)
	dayReqs.add(v, ok)
	row.ErrorRate24hPct = errorRatePct(dayErrs, dayReqs)
	if topSeverity != "" {
		s := topSeverity
		row.TopSeverity = &s
	}
	return row
}

// BuildFleet totals every tenant's usage over the window; activeTenants counts tenants with the active status.
func BuildFleet(infos []TenantInfo, x *ValueIndex, w Window, activeStatus string) FleetTotals {
	var cur, prev windowTotals
	trend := make([]float64, w.Days)
	active := 0
	for _, info := range infos {
		if info.Status == activeStatus {
			active++
		}
		c, p := totalsFor(x, info.ID, w), totalsFor(x, info.ID, w.Prev())
		cur.compute.add(c.compute.v, c.compute.ok)
		cur.db.add(c.db.v, c.db.ok)
		cur.r2.add(c.r2.v, c.r2.ok)
		cur.tokens.add(c.tokens.v, c.tokens.ok)
		prev.compute.add(p.compute.v, p.compute.ok)
		prev.db.add(p.db.v, p.db.ok)
		prev.r2.add(p.r2.v, p.r2.ok)
		prev.tokens.add(p.tokens.v, p.tokens.ok)
		for i, v := range ComputeTrend(x, info.ID, w) {
			trend[i] += v
		}
	}
	return FleetTotals{
		ActiveTenants: active, TotalTenants: len(infos),
		ComputeHours: cur.compute.v, ComputeHoursPrev: prev.compute.ptr(),
		DBBytes: cur.db.v, DBBytesPrev: prev.db.ptr(),
		R2Bytes: cur.r2.v, R2BytesPrev: prev.r2.ptr(),
		AITokens: cur.tokens.v, AITokensPrev: prev.tokens.ptr(),
		ComputeTrend: trend,
	}
}
