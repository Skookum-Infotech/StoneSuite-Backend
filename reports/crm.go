package reports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// CRM reports run on the relational (V2) CRM design, where leads, prospects and
// customers are rows of one `customer` table split by record type, and a
// conversion creates a CHILD row linked by customer_parent_id
// (lead -> prospect -> customer). Tenants still on the V1 JSONB design are
// refused by the controller.

const (
	typeLead     = "LEAD"
	typeProspect = "PROS"
	typeCustomer = "CUST"

	statusLeadQualified   = "LQUA"
	statusLeadUnqualified = "LUNQ"
	statusProspectLost    = "PCLL"

	// UnspecifiedSource labels leads that have no lead source set.
	UnspecifiedSource = "Unspecified"

	// DefaultStaleDays / MinStaleDays / MaxStaleDays bound the idle-days
	// threshold of the stale report.
	DefaultStaleDays = 14
	MinStaleDays     = 1
	MaxStaleDays     = 365

	secondsPerDay = 86400.0
)

// CRMFilter is the lead-creation window and scope the CRM cohort reports share.
// Dates are inclusive; a zero value is unbounded on that side.
type CRMFilter struct {
	From, To        time.Time
	Scope           authz.Scope
	ActorIdentityID string
}

// leadCohort builds the CTE chain every cohort report shares: `lead_cohort` (leads
// created in the window, in the caller's scope), `descendants` (every record
// converted from them, however deep), `journey` (one row per lead with when it
// first reached prospect/customer) and `rev` (billed net revenue of each
// lead's customers). Placeholder $1 is the billed-invoice status list.
func leadCohort(ctx context.Context, pool *pgxpool.Pool, f CRMFilter) (cte string, args []any, ok bool, err error) {
	args = []any{invoiceBilledCodes}
	where := []string{"c.customer_deleted_at IS NULL"}
	if f.Scope != authz.ScopeAll {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, f.ActorIdentityID)
		if !found {
			return "", nil, false, nil
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("c.customer_crm_owner_user_id = $%d", len(args)))
	}
	if !f.From.IsZero() {
		args = append(args, f.From)
		where = append(where, fmt.Sprintf("c.customer_created_at >= $%d", len(args)))
	}
	if !f.To.IsZero() {
		args = append(args, f.To.AddDate(0, 0, 1))
		where = append(where, fmt.Sprintf("c.customer_created_at < $%d", len(args)))
	}
	cte = fmt.Sprintf(`
		WITH RECURSIVE lead_cohort AS (
			SELECT c.customer_id, c.customer_created_at, c.customer_lead_source, cs.crm_status_code
			FROM customer c
			JOIN lkp_record_type rt ON rt.record_type_id = c.record_type AND rt.record_type_code = '%[1]s'
			LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = c.customer_crm_status
			WHERE %[2]s
		), descendants AS (
			SELECT l.customer_id AS lead_id, ch.customer_id, ch.customer_created_at, rt.record_type_code
			FROM lead_cohort l
			JOIN customer ch ON ch.customer_parent_id = l.customer_id AND ch.customer_deleted_at IS NULL
			JOIN lkp_record_type rt ON rt.record_type_id = ch.record_type
			UNION ALL
			SELECT d.lead_id, ch.customer_id, ch.customer_created_at, rt.record_type_code
			FROM descendants d
			JOIN customer ch ON ch.customer_parent_id = d.customer_id AND ch.customer_deleted_at IS NULL
			JOIN lkp_record_type rt ON rt.record_type_id = ch.record_type
		), journey AS (
			SELECT l.customer_id, l.customer_created_at, l.customer_lead_source, l.crm_status_code,
			       MIN(d.customer_created_at) FILTER (WHERE d.record_type_code = '%[3]s') AS prospect_at,
			       MIN(d.customer_created_at) FILTER (WHERE d.record_type_code = '%[4]s') AS customer_at
			FROM lead_cohort l
			LEFT JOIN descendants d ON d.lead_id = l.customer_id
			GROUP BY l.customer_id, l.customer_created_at, l.customer_lead_source, l.crm_status_code
		), rev AS (
			SELECT d.lead_id, SUM(i.invoice_subtotal - i.invoice_discount_total) AS revenue
			FROM descendants d
			JOIN invoice i ON i.invoice_customer_id = d.customer_id AND i.invoice_deleted_at IS NULL
			JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
			WHERE d.record_type_code = '%[4]s' AND rs.record_status_code = ANY($1)
			GROUP BY d.lead_id
		)`, typeLead, strings.Join(where, " AND "), typeProspect, typeCustomer)
	return cte, args, true, nil
}

// invoiceBilledCodes mirrors the Sales reports' "billed" invoice statuses.
var invoiceBilledCodes = invoicesSpec.statusCodes

// reachedProspect is true for a lead that became a prospect, or went straight
// to customer: either way it is past the prospect stage.
const (
	reachedProspectExpr = "(j.prospect_at IS NOT NULL OR j.customer_at IS NOT NULL)"
	reachedCustomerExpr = "(j.customer_at IS NOT NULL)"
	// A lead counts as qualified if it is Qualified now, or it was converted
	// (conversion requires Qualified), so a converted lead whose own status
	// later changed still counts.
	qualifiedExpr = "(j.crm_status_code = '" + statusLeadQualified + "' OR " + reachedProspectExpr + ")"
)

// FunnelStage is one step of the Conversion Funnel.
type FunnelStage struct {
	Key             string   `json:"key"`
	Label           string   `json:"label"`
	Count           int      `json:"count"`
	PercentOfLeads  float64  `json:"percentOfLeads"`
	PercentOfPrev   float64  `json:"percentOfPrevious"`
	AvgDaysFromLead *float64 `json:"avgDaysFromLead"`
}

// Funnel is the Conversion Funnel report.
type Funnel struct {
	Stages      []FunnelStage `json:"stages"`
	Unqualified int           `json:"unqualified"`
}

// funnelCounts are the raw cohort counts the funnel is built from.
type funnelCounts struct {
	leads, qualified, unqualified, prospects, customers int
	daysToProspect, daysToCustomer                      *float64
}

func percent(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole) * 100
}

// buildFunnel turns cohort counts into ordered stages with percentages of the
// lead cohort and of the previous stage.
func buildFunnel(c funnelCounts) Funnel {
	steps := []struct {
		key, label string
		count      int
		days       *float64
	}{
		{"leads", "Leads created", c.leads, nil},
		{"qualified", "Qualified", c.qualified, nil},
		{"prospects", "Became prospects", c.prospects, c.daysToProspect},
		{"customers", "Became customers", c.customers, c.daysToCustomer},
	}
	out := Funnel{Stages: make([]FunnelStage, 0, len(steps)), Unqualified: c.unqualified}
	prev := c.leads
	for i, s := range steps {
		st := FunnelStage{Key: s.key, Label: s.label, Count: s.count, AvgDaysFromLead: s.days}
		st.PercentOfLeads = percent(s.count, c.leads)
		if i == 0 {
			st.PercentOfPrev = percent(s.count, s.count)
		} else {
			st.PercentOfPrev = percent(s.count, prev)
		}
		prev = s.count
		out.Stages = append(out.Stages, st)
	}
	return out
}

// ConversionFunnel counts the leads created in the window and how far each
// got: qualified, prospect, customer, plus the average days to get there.
func ConversionFunnel(ctx context.Context, pool *pgxpool.Pool, f CRMFilter) (Funnel, error) {
	cte, args, ok, err := leadCohort(ctx, pool, f)
	if err != nil {
		return Funnel{}, err
	}
	if !ok {
		return buildFunnel(funnelCounts{}), nil
	}
	q := cte + fmt.Sprintf(`
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE %[1]s),
		       COUNT(*) FILTER (WHERE j.crm_status_code = '%[2]s'),
		       COUNT(*) FILTER (WHERE %[3]s),
		       COUNT(*) FILTER (WHERE %[4]s),
		       AVG(EXTRACT(EPOCH FROM (j.prospect_at - j.customer_created_at)) / %[5]f) FILTER (WHERE j.prospect_at IS NOT NULL),
		       AVG(EXTRACT(EPOCH FROM (j.customer_at - j.customer_created_at)) / %[5]f) FILTER (WHERE j.customer_at IS NOT NULL)
		FROM journey j`, qualifiedExpr, statusLeadUnqualified, reachedProspectExpr, reachedCustomerExpr, secondsPerDay)
	var c funnelCounts
	if err := pool.QueryRow(ctx, q, args...).Scan(
		&c.leads, &c.qualified, &c.unqualified, &c.prospects, &c.customers, &c.daysToProspect, &c.daysToCustomer,
	); err != nil {
		return Funnel{}, fmt.Errorf("query conversion funnel: %w", err)
	}
	return buildFunnel(c), nil
}

// SourceRow is one lead source's row in Lead Source Performance.
type SourceRow struct {
	Source         string  `json:"source"`
	Leads          int     `json:"leads"`
	Qualified      int     `json:"qualified"`
	Prospects      int     `json:"prospects"`
	Customers      int     `json:"customers"`
	ConversionPct  float64 `json:"conversionPercent"`
	Revenue        float64 `json:"revenue"`
	RevenuePerLead float64 `json:"revenuePerLead"`
}

// SourceTotals sums the source table.
type SourceTotals struct {
	Leads         int     `json:"leads"`
	Customers     int     `json:"customers"`
	ConversionPct float64 `json:"conversionPercent"`
	Revenue       float64 `json:"revenue"`
}

// finishSourceRow fills the derived columns of a source row.
func finishSourceRow(r SourceRow) SourceRow {
	r.ConversionPct = percent(r.Customers, r.Leads)
	if r.Leads > 0 {
		r.RevenuePerLead = r.Revenue / float64(r.Leads)
	}
	return r
}

// sumSources totals the rows.
func sumSources(rows []SourceRow) SourceTotals {
	var t SourceTotals
	for _, r := range rows {
		t.Leads += r.Leads
		t.Customers += r.Customers
		t.Revenue += r.Revenue
	}
	t.ConversionPct = percent(t.Customers, t.Leads)
	return t
}

// LeadSourcePerformance groups the lead cohort by source. Revenue is the
// billed net revenue (all time, not just the window) of the customers those
// leads became.
func LeadSourcePerformance(ctx context.Context, pool *pgxpool.Pool, f CRMFilter) ([]SourceRow, SourceTotals, error) {
	rows := []SourceRow{}
	cte, args, ok, err := leadCohort(ctx, pool, f)
	if err != nil || !ok {
		return rows, SourceTotals{}, err
	}
	q := cte + fmt.Sprintf(`
		SELECT COALESCE(ls.lead_source_name, '%[1]s'),
		       COUNT(*),
		       COUNT(*) FILTER (WHERE %[2]s),
		       COUNT(*) FILTER (WHERE %[3]s),
		       COUNT(*) FILTER (WHERE %[4]s),
		       COALESCE(SUM(rev.revenue), 0)
		FROM journey j
		LEFT JOIN lkp_crm_lead_source ls ON ls.lead_source_id = j.customer_lead_source
		LEFT JOIN rev ON rev.lead_id = j.customer_id
		GROUP BY 1
		ORDER BY COALESCE(SUM(rev.revenue), 0) DESC, COUNT(*) DESC, 1
		LIMIT %[5]d`, UnspecifiedSource, qualifiedExpr, reachedProspectExpr, reachedCustomerExpr, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, SourceTotals{}, fmt.Errorf("query lead sources: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row SourceRow
		if err := r.Scan(&row.Source, &row.Leads, &row.Qualified, &row.Prospects, &row.Customers, &row.Revenue); err != nil {
			return nil, SourceTotals{}, fmt.Errorf("scan lead source: %w", err)
		}
		rows = append(rows, finishSourceRow(row))
	}
	if err := r.Err(); err != nil {
		return nil, SourceTotals{}, fmt.Errorf("query lead sources: %w", err)
	}
	return rows, sumSources(rows), nil
}

// StaleRecord is one open lead or prospect with no recent activity.
type StaleRecord struct {
	ID        string    `json:"id"`
	Stage     string    `json:"stage"` // lead | prospect
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Owner     string    `json:"owner"`
	LastTouch time.Time `json:"lastTouch"`
	DaysIdle  int       `json:"daysIdle"`
}

// ClampStaleDays keeps the idle threshold inside its allowed range.
func ClampStaleDays(days int) int {
	if days < MinStaleDays {
		return MinStaleDays
	}
	if days > MaxStaleDays {
		return MaxStaleDays
	}
	return days
}

// StaleRecords lists open leads and prospects whose last logged activity or
// status change is at least `days` days old, oldest first. "Open" means not
// converted (no child record), and neither an Unqualified lead nor a Lost
// prospect. Not window-based: it is a worklist as of today.
func StaleRecords(ctx context.Context, pool *pgxpool.Pool, days int, scope authz.Scope, actorIdentityID string) ([]StaleRecord, error) {
	out := []StaleRecord{}
	args := []any{ClampStaleDays(days)}
	ownerFilter := ""
	if scope != authz.ScopeAll {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return out, nil
		}
		args = append(args, empID)
		ownerFilter = fmt.Sprintf(" AND c.customer_crm_owner_user_id = $%d", len(args))
	}
	q := fmt.Sprintf(`
		SELECT c.customer_uuid::text, rt.record_type_code, c.customer_name, COALESCE(cs.crm_status_name, ''),
		       COALESCE(NULLIF(TRIM(e.employee_first_name || ' ' || e.employee_last_name), ''), ''),
		       t.last_touch, (CURRENT_DATE - t.last_touch::date)
		FROM customer c
		JOIN lkp_record_type rt ON rt.record_type_id = c.record_type AND rt.record_type_code IN ('%[1]s', '%[2]s')
		LEFT JOIN lkp_crm_status cs ON cs.crm_status_id = c.customer_crm_status
		LEFT JOIN employee e ON e.employee_id = c.customer_crm_owner_user_id
		CROSS JOIN LATERAL (
			SELECT GREATEST(
				c.customer_created_at,
				COALESCE((SELECT MAX(a.occurred_at) FROM crm_activity a WHERE a.customer_id = c.customer_id AND a.deleted_at IS NULL), c.customer_created_at),
				COALESCE((SELECT MAX(h.at) FROM customer_history h WHERE h.customer_id = c.customer_id), c.customer_created_at)
			) AS last_touch
		) t
		WHERE c.customer_deleted_at IS NULL%[3]s
		  AND COALESCE(cs.crm_status_code, '') NOT IN ('%[4]s', '%[5]s')
		  AND NOT EXISTS (SELECT 1 FROM customer ch WHERE ch.customer_parent_id = c.customer_id AND ch.customer_deleted_at IS NULL)
		  AND (CURRENT_DATE - t.last_touch::date) >= $1
		ORDER BY t.last_touch ASC, c.customer_name
		LIMIT %[6]d`, typeLead, typeProspect, ownerFilter, statusLeadUnqualified, statusProspectLost, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query stale records: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var rec StaleRecord
		var code string
		if err := r.Scan(&rec.ID, &code, &rec.Name, &rec.Status, &rec.Owner, &rec.LastTouch, &rec.DaysIdle); err != nil {
			return nil, fmt.Errorf("scan stale record: %w", err)
		}
		rec.Stage = "lead"
		if code == typeProspect {
			rec.Stage = "prospect"
		}
		out = append(out, rec)
	}
	if err := r.Err(); err != nil {
		return nil, fmt.Errorf("query stale records: %w", err)
	}
	return out, nil
}
