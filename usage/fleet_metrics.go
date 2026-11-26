package usage

import "time"

// Metric keys stored in tenant_usage_daily.metric_key.
const (
	MetricComputeRequestHours = "compute_request_hours"
	MetricComputeJobHours     = "compute_job_hours"
	MetricRequests            = "requests"
	MetricErrors5xx           = "errors_5xx"
	MetricP95Ms               = "p95_ms"
	MetricDBBytes             = "db_bytes"
	MetricR2Bytes             = "r2_bytes"
	MetricR2Objects           = "r2_objects"
	MetricActiveUsers         = "active_users"
	MetricEmailsSent          = "emails_sent"
	MetricAutomationRuns      = "automation_runs"
	MetricFailedJobs          = "failed_jobs"
	MetricDeadJobs            = "dead_jobs"
	// MetricAITokens is read from tenant_llm_usage_daily, never copied into tenant_usage_daily.
	MetricAITokens = "ai_tokens"
	// MetricRecordsCreatedPrefix prefixes the per-resource records-created metric keys.
	MetricRecordsCreatedPrefix = "records_created."
)

// Alert severities.
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
	SeverityInfo     = "info"
)

// Alert statuses.
const (
	AlertStatusOpen     = "open"
	AlertStatusAcked    = "acked"
	AlertStatusSnoozed  = "snoozed"
	AlertStatusResolved = "resolved"
)

// HourlyRow is one tenant-hour of request metering.
type HourlyRow struct {
	TenantID  string
	Hour      time.Time
	Requests  int64
	Errors5xx int64
	HandlerMs int64
	P95Ms     int
}

// DailyValue is one tenant-day value of a metric.
type DailyValue struct {
	TenantID string
	Day      time.Time
	Key      string
	Value    float64
}

// TableSize is the measured size of one table in a tenant database.
type TableSize struct {
	TableName   string
	TotalBytes  int64
	RowEstimate int64
}

// Alert is a usage alert row, joined with the tenant's display name.
type Alert struct {
	ID, TenantID, TenantName, RuleKey, MetricKey string
	Severity, Status, Message, Detail            string
	Observed, Baseline                           *float64
	FirstSeenAt, LastSeenAt                      time.Time
	SnoozedUntil                                 *time.Time
}

// AlertFiring is a rule firing to be recorded as (or merged into) a live alert.
type AlertFiring struct {
	TenantID, RuleKey, MetricKey string
	Severity, Message, Detail    string
	Observed                     float64
	Baseline                     *float64
}

// RecordsCreatedKey returns the metric key for records created of a resource.
func RecordsCreatedKey(resource string) string {
	return MetricRecordsCreatedPrefix + resource
}
