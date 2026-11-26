package usage

import (
	"sort"
	"strings"
	"time"
)

// AlertHistoryDays is how far back the tenant detail lists alerts.
const AlertHistoryDays = 90

// resourceLabels names resources whose label is not just the title-cased, pluralized key.
var resourceLabels = map[string]string{
	"inventory": "Inventory Items",
	"crm":       "CRM Records",
}

// ResourceLabel turns a records-created resource key ("sales_order") into a UI label ("Sales Orders").
func ResourceLabel(resource string) string {
	if label, ok := resourceLabels[resource]; ok {
		return label
	}
	words := strings.FieldsFunc(resource, func(r rune) bool { return r == '_' || r == '-' || r == ' ' })
	if len(words) == 0 {
		return resource
	}
	for i, w := range words {
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	last := words[len(words)-1]
	switch {
	case strings.HasSuffix(last, "s"):
	case strings.HasSuffix(last, "y") && len(last) > 1 && !strings.ContainsRune("aeiouAEIOU", rune(last[len(last)-2])):
		words[len(words)-1] = last[:len(last)-1] + "ies"
	case strings.HasSuffix(last, "x"), strings.HasSuffix(last, "ch"), strings.HasSuffix(last, "sh"):
		words[len(words)-1] = last + "es"
	default:
		words[len(words)-1] = last + "s"
	}
	return strings.Join(words, " ")
}

// TokenSource is one source's token total.
type TokenSource struct {
	Source      string `json:"source"`
	TotalTokens int64  `json:"totalTokens"`
}

// DetailMetrics is the tenant detail "metrics" object.
type DetailMetrics struct {
	ComputeRequestHours float64       `json:"computeRequestHours"`
	ComputeJobHours     float64       `json:"computeJobHours"`
	DBShareOfFleetPct   *float64      `json:"dbShareOfFleetPct"`
	R2Objects           *int64        `json:"r2Objects"`
	AITokensBySource    []TokenSource `json:"aiTokensBySource"`
	DauMauPct           *float64      `json:"dauMauPct"`
	LastSignInAt        *time.Time    `json:"lastSignInAt"`
	Requests            int64         `json:"requests"`
	RequestsPrev        *int64        `json:"requestsPrev"`
	P95LatencyMs        *float64      `json:"p95LatencyMs"`
	FailedJobs          int64         `json:"failedJobs"`
	DeadJobs            int64         `json:"deadJobs"`
}

// SeriesPoint is one day of a tenant's usage series.
type SeriesPoint struct {
	Date                string  `json:"date"`
	ComputeRequestHours float64 `json:"computeRequestHours"`
	ComputeJobHours     float64 `json:"computeJobHours"`
	DBBytes             float64 `json:"dbBytes"`
	R2Bytes             float64 `json:"r2Bytes"`
}

// TableSizeView is the JSON form of a TableSize.
type TableSizeView struct {
	TableName   string `json:"tableName"`
	TotalBytes  int64  `json:"totalBytes"`
	RowEstimate int64  `json:"rowEstimate"`
}

// ModuleCount is records created in one module.
type ModuleCount struct {
	Label string `json:"label"`
	Count int64  `json:"count"`
}

// Activity is the tenant detail "activity" object.
type Activity struct {
	RecordsByModule []ModuleCount `json:"recordsByModule"`
	EmailsSent      int64         `json:"emailsSent"`
	AutomationRuns  int64         `json:"automationRuns"`
}

// TenantDetail is the tenant detail body (without the success flag).
type TenantDetail struct {
	GeneratedAt         time.Time       `json:"generatedAt"`
	Period              string          `json:"period"`
	Tenant              TenantUsageRow  `json:"tenant"`
	Metrics             DetailMetrics   `json:"metrics"`
	Series              []SeriesPoint   `json:"series"`
	TopTables           []TableSizeView `json:"topTables"`
	TopTablesMeasuredAt *time.Time      `json:"topTablesMeasuredAt"`
	Activity            Activity        `json:"activity"`
	Alerts              []AlertView     `json:"alerts"`
}

// DetailInput is everything BuildDetail needs; it reads nothing else.
type DetailInput struct {
	Now      time.Time
	Period   string
	Info     TenantInfo
	Window   Window
	Values   *ValueIndex // the tenant's daily values covering its window, the previous window and 30 days
	Sign     SignInStats // over the window
	Sign30   SignInStats // active users over the last 30 days
	TopSev   string
	FleetDB  float64 // sum of every tenant's last db_bytes in the window
	Tables   []TableSize
	TablesAt *time.Time
	Tokens   map[string]int64
	Alerts   []Alert
}

// SumLastAcross adds each tenant's latest value of metric in [from, to] (tenants without data add nothing).
func SumLastAcross(x *ValueIndex, tenantIDs []string, metric string, from, to time.Time) float64 {
	var total float64
	for _, id := range tenantIDs {
		if v, ok := x.Last(id, metric, from, to); ok {
			total += v
		}
	}
	return total
}

// BuildDetail assembles the tenant detail body.
func BuildDetail(in DetailInput) TenantDetail {
	x, w, id := in.Values, in.Window, in.Info.ID
	from, to := w.Start(), w.End
	sum := func(metric string) float64 {
		v, _ := x.Sum(id, metric, from, to)
		return v
	}
	m := DetailMetrics{
		ComputeRequestHours: sum(MetricComputeRequestHours),
		ComputeJobHours:     sum(MetricComputeJobHours),
		AITokensBySource:    tokenSources(in.Tokens),
		LastSignInAt:        in.Sign.LastSignInAt,
		Requests:            int64(sum(MetricRequests)),
		FailedJobs:          int64(sum(MetricFailedJobs)),
		DeadJobs:            int64(sum(MetricDeadJobs)),
	}
	if db, ok := x.Last(id, MetricDBBytes, from, to); ok && in.FleetDB > 0 {
		share := db / in.FleetDB * percentFactor
		m.DBShareOfFleetPct = &share
	}
	if v, ok := x.Last(id, MetricR2Objects, from, to); ok {
		n := int64(v)
		m.R2Objects = &n
	}
	if v, ok := x.Sum(id, MetricRequests, w.Prev().Start(), w.Prev().End); ok {
		n := int64(v)
		m.RequestsPrev = &n
	}
	if v, ok := x.Max(id, MetricP95Ms, from, to); ok {
		m.P95LatencyMs = &v
	}
	if mean, ok := x.Mean(id, MetricActiveUsers, to.AddDate(0, 0, -(dauMauDays-1)), to); ok && in.Sign30.ActiveUsers > 0 {
		pct := mean / float64(in.Sign30.ActiveUsers) * percentFactor
		m.DauMauPct = &pct
	}
	tables := make([]TableSizeView, 0, len(in.Tables))
	for _, t := range in.Tables {
		tables = append(tables, TableSizeView{TableName: t.TableName, TotalBytes: t.TotalBytes, RowEstimate: t.RowEstimate})
	}
	return TenantDetail{
		GeneratedAt: in.Now.UTC(), Period: in.Period,
		Tenant:  BuildRow(in.Info, x, w, in.Sign, in.TopSev),
		Metrics: m, Series: buildSeries(x, id, w),
		TopTables: tables, TopTablesMeasuredAt: in.TablesAt,
		Activity: buildActivity(x, id, w),
		Alerts:   AlertViews(in.Alerts),
	}
}

func tokenSources(by map[string]int64) []TokenSource {
	out := make([]TokenSource, 0, len(by))
	for src, n := range by {
		out = append(out, TokenSource{Source: src, TotalTokens: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func buildSeries(x *ValueIndex, tenant string, w Window) []SeriesPoint {
	dates := w.Dates()
	out := make([]SeriesPoint, 0, len(dates))
	for _, d := range dates {
		p := SeriesPoint{Date: d.Format(dateLayout)}
		p.ComputeRequestHours, _ = x.At(tenant, MetricComputeRequestHours, d)
		p.ComputeJobHours, _ = x.At(tenant, MetricComputeJobHours, d)
		p.DBBytes, _ = x.At(tenant, MetricDBBytes, d)
		p.R2Bytes, _ = x.At(tenant, MetricR2Bytes, d)
		out = append(out, p)
	}
	return out
}

func buildActivity(x *ValueIndex, tenant string, w Window) Activity {
	from, to := w.Start(), w.End
	a := Activity{RecordsByModule: []ModuleCount{}}
	emails, _ := x.Sum(tenant, MetricEmailsSent, from, to)
	runs, _ := x.Sum(tenant, MetricAutomationRuns, from, to)
	a.EmailsSent, a.AutomationRuns = int64(emails), int64(runs)
	for k := range x.vals {
		if k.tenant != tenant || !strings.HasPrefix(k.metric, MetricRecordsCreatedPrefix) {
			continue
		}
		n, ok := x.Sum(tenant, k.metric, from, to)
		if !ok || n == 0 {
			continue
		}
		a.RecordsByModule = append(a.RecordsByModule, ModuleCount{
			Label: ResourceLabel(strings.TrimPrefix(k.metric, MetricRecordsCreatedPrefix)), Count: int64(n),
		})
	}
	sort.Slice(a.RecordsByModule, func(i, j int) bool {
		if a.RecordsByModule[i].Count != a.RecordsByModule[j].Count {
			return a.RecordsByModule[i].Count > a.RecordsByModule[j].Count
		}
		return a.RecordsByModule[i].Label < a.RecordsByModule[j].Label
	})
	return a
}
