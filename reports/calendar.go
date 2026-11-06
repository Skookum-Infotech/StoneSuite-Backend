package reports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/salesorder"
)

// Calendar reports read the order schedule (sales_order_schedule, the 11
// planned activities) and install appointments (real visits booked on
// fabrication jobs). Definitions follow the Activity Planner:
//   - OVERDUE is not stored: a 'pending' activity whose scheduled time has
//     passed (one someone has started is not late, a completed one is done);
//   - cancelled orders are left out;
//   - scope is the sales order's owner, like the Sales reports.

const (
	// DefaultUpcomingWeeks / MaxUpcomingWeeks bound the Upcoming Installs horizon.
	DefaultUpcomingWeeks = 8
	MaxUpcomingWeeks     = 26
	daysPerWeek          = 7

	cancelledOrderCode = "CANC"
	// ActivityCreatedCode is completed automatically when an order is made, so
	// it can never be late and is left out of the activity reports.
	activityCreatedCode = "created"

	sourceAppointment = "appointment"
	sourcePlan        = "plan"
)

// nowUTC is the wall-clock "now" the schedule is compared against: scheduled
// times are stored zone-less in UTC by convention (see salesorder/schedule.go).
const nowUTC = "(NOW() AT TIME ZONE 'UTC')"

// activityNames maps a schedule activity code to its display name, from the
// planner's own catalog so the reports can never drift from it.
func activityNames() map[string]string {
	out := map[string]string{}
	for _, a := range salesorder.CatalogActivities() {
		out[a.Code] = a.Name
	}
	return out
}

// ClampWeeks keeps the upcoming-installs horizon inside its allowed range.
func ClampWeeks(weeks int) int {
	if weeks < 1 {
		return 1
	}
	if weeks > MaxUpcomingWeeks {
		return MaxUpcomingWeeks
	}
	return weeks
}

// calendarScope returns the " AND so.sales_order_owner_id = $n" predicate for a
// non-"all" scope; ok=false when the caller has no employee record.
func calendarScope(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, actor string, args *[]any) (string, bool) {
	return ownerClause(ctx, pool, scope, actor, "so.sales_order_owner_id", args)
}

// UpcomingInstall is one install on the calendar ahead.
type UpcomingInstall struct {
	OrderID   string    `json:"orderId"`
	Order     string    `json:"order"`
	Customer  string    `json:"customer"`
	Day       time.Time `json:"day"`
	WeekStart time.Time `json:"weekStart"`
	Source    string    `json:"source"` // appointment | plan
	Lead      string    `json:"lead"`
}

// WeekCount is the number of installs starting in one week.
type WeekCount struct {
	WeekStart time.Time `json:"weekStart"`
	Count     int       `json:"count"`
}

// weekStart is the Monday on or before d, at midnight UTC.
func weekStart(d time.Time) time.Time {
	d = time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC)
	offset := (int(d.Weekday()) + daysPerWeek - int(time.Monday)) % daysPerWeek
	return d.AddDate(0, 0, -offset)
}

// weeklyCounts buckets installs by Monday-start week, oldest first, and fills
// the empty weeks in between so the chart shows gaps as zero.
func weeklyCounts(rows []UpcomingInstall) []WeekCount {
	if len(rows) == 0 {
		return []WeekCount{}
	}
	counts := map[time.Time]int{}
	first, last := weekStart(rows[0].Day), weekStart(rows[0].Day)
	for _, r := range rows {
		w := weekStart(r.Day)
		counts[w]++
		if w.Before(first) {
			first = w
		}
		if w.After(last) {
			last = w
		}
	}
	out := []WeekCount{}
	for w := first; !w.After(last); w = w.AddDate(0, 0, daysPerWeek) {
		out = append(out, WeekCount{WeekStart: w, Count: counts[w]})
	}
	return out
}

// UpcomingInstalls lists installs in the next `weeks` weeks (from today). A real
// scheduled install appointment is shown as such; an order with none falls back
// to its planned Installation activity, so nothing is counted twice.
func UpcomingInstalls(ctx context.Context, pool *pgxpool.Pool, weeks int, scope authz.Scope, actor string) ([]UpcomingInstall, []WeekCount, error) {
	rows := []UpcomingInstall{}
	args := []any{ClampWeeks(weeks) * daysPerWeek}
	owner, ok := calendarScope(ctx, pool, scope, actor, &args)
	if !ok {
		return rows, []WeekCount{}, nil
	}
	hasAppointment := `EXISTS (
		SELECT 1 FROM install_appointment x
		JOIN fabrication_job xj ON xj.fabrication_job_id = x.fabrication_job_id
		WHERE xj.sales_order_id = so.sales_order_id AND x.visit_type = 'install' AND x.appointment_status = 'scheduled')`
	q := fmt.Sprintf(`
		SELECT order_uuid, order_number, customer, day, source, crew_lead FROM (
			SELECT so.sales_order_uuid::text AS order_uuid, COALESCE(so.sales_order_number, '') AS order_number,
			       COALESCE(c.customer_name, '') AS customer, ia.local_start_date AS day, '%[1]s' AS source,
			       COALESCE((SELECT TRIM(e.employee_first_name || ' ' || e.employee_last_name)
			                 FROM install_appointment_crew iac JOIN employee e ON e.employee_id = iac.employee_id
			                 WHERE iac.install_appointment_id = ia.install_appointment_id AND iac.crew_role = 'lead'), '') AS crew_lead
			FROM install_appointment ia
			JOIN fabrication_job fj ON fj.fabrication_job_id = ia.fabrication_job_id
			JOIN sales_order so ON so.sales_order_id = fj.sales_order_id
			JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
			JOIN customer c ON c.customer_id = so.sales_order_customer_id
			WHERE ia.visit_type = 'install' AND ia.appointment_status = 'scheduled'
			  AND ia.local_start_date BETWEEN CURRENT_DATE AND CURRENT_DATE + $1::int
			  AND so.sales_order_deleted_at IS NULL AND rs.record_status_code <> '%[3]s'%[4]s
			UNION ALL
			SELECT so.sales_order_uuid::text, COALESCE(so.sales_order_number, ''), COALESCE(c.customer_name, ''),
			       s.scheduled_at::date, '%[2]s', ''
			FROM sales_order_schedule s
			JOIN sales_order so ON so.sales_order_id = s.sales_order_id
			JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
			JOIN customer c ON c.customer_id = so.sales_order_customer_id
			WHERE s.activity_code = '%[5]s' AND s.schedule_status <> 'completed'
			  AND s.scheduled_at::date BETWEEN CURRENT_DATE AND CURRENT_DATE + $1::int
			  AND so.sales_order_deleted_at IS NULL AND rs.record_status_code <> '%[3]s'%[4]s
			  AND NOT %[6]s
		) u
		ORDER BY day, order_number
		LIMIT %[7]d`, sourceAppointment, sourcePlan, cancelledOrderCode, owner, salesorder.ActivityInstallation, hasAppointment, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("query upcoming installs: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row UpcomingInstall
		if err := r.Scan(&row.OrderID, &row.Order, &row.Customer, &row.Day, &row.Source, &row.Lead); err != nil {
			return nil, nil, fmt.Errorf("scan upcoming install: %w", err)
		}
		row.WeekStart = weekStart(row.Day)
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, nil, fmt.Errorf("query upcoming installs: %w", err)
	}
	return rows, weeklyCounts(rows), nil
}

// OverdueActivity is one pending activity past its scheduled time.
type OverdueActivity struct {
	OrderID     string    `json:"orderId"`
	Order       string    `json:"order"`
	Customer    string    `json:"customer"`
	OrderStatus string    `json:"orderStatus"`
	Code        string    `json:"code"`
	Activity    string    `json:"activity"`
	ScheduledAt time.Time `json:"scheduledAt"`
	DaysOverdue int       `json:"daysOverdue"`
}

// ActivityCount is a per-activity tally, in plan order.
type ActivityCount struct {
	Code     string `json:"code"`
	Activity string `json:"activity"`
	Count    int    `json:"count"`
}

// OverdueTotals summarises the overdue list.
type OverdueTotals struct {
	Activities int             `json:"activities"`
	Orders     int             `json:"orders"`
	Oldest     int             `json:"oldestDays"`
	ByActivity []ActivityCount `json:"byActivity"`
}

// sumOverdue tallies overdue activities per activity (plan order), distinct
// orders and the oldest lateness.
func sumOverdue(rows []OverdueActivity) OverdueTotals {
	orders := map[string]bool{}
	byCode := map[string]int{}
	t := OverdueTotals{Activities: len(rows), ByActivity: []ActivityCount{}}
	for _, r := range rows {
		orders[r.OrderID] = true
		byCode[r.Code]++
		if r.DaysOverdue > t.Oldest {
			t.Oldest = r.DaysOverdue
		}
	}
	t.Orders = len(orders)
	for _, a := range salesorder.CatalogActivities() {
		if n := byCode[a.Code]; n > 0 {
			t.ByActivity = append(t.ByActivity, ActivityCount{Code: a.Code, Activity: a.Name, Count: n})
		}
	}
	return t
}

// OverdueActivities lists pending schedule activities whose time has passed,
// most overdue first (ties in plan order).
func OverdueActivities(ctx context.Context, pool *pgxpool.Pool, scope authz.Scope, actor string) ([]OverdueActivity, OverdueTotals, error) {
	rows := []OverdueActivity{}
	args := []any{}
	owner, ok := calendarScope(ctx, pool, scope, actor, &args)
	if !ok {
		return rows, sumOverdue(nil), nil
	}
	names := activityNames()
	q := fmt.Sprintf(`
		SELECT so.sales_order_uuid::text, COALESCE(so.sales_order_number, ''), COALESCE(c.customer_name, ''),
		       rs.record_status_name, s.activity_code, s.scheduled_at,
		       GREATEST(%[1]s::date - s.scheduled_at::date, 0)
		FROM sales_order_schedule s
		JOIN sales_order so ON so.sales_order_id = s.sales_order_id
		JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		JOIN customer c ON c.customer_id = so.sales_order_customer_id
		WHERE s.schedule_status = 'pending' AND s.scheduled_at < %[1]s AND s.activity_code <> '%[2]s'
		  AND so.sales_order_deleted_at IS NULL AND rs.record_status_code <> '%[3]s'%[4]s
		ORDER BY 7 DESC, s.activity_sequence, so.sales_order_number
		LIMIT %[5]d`, nowUTC, activityCreatedCode, cancelledOrderCode, owner, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, OverdueTotals{}, fmt.Errorf("query overdue activities: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row OverdueActivity
		if err := r.Scan(&row.OrderID, &row.Order, &row.Customer, &row.OrderStatus, &row.Code, &row.ScheduledAt, &row.DaysOverdue); err != nil {
			return nil, OverdueTotals{}, fmt.Errorf("scan overdue activity: %w", err)
		}
		row.Activity = nameOrCode(names, row.Code)
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, OverdueTotals{}, fmt.Errorf("query overdue activities: %w", err)
	}
	return rows, sumOverdue(rows), nil
}

func nameOrCode(names map[string]string, code string) string {
	if n, ok := names[code]; ok {
		return n
	}
	return code
}

// OnTimeRow is one activity's completion record in the window.
type OnTimeRow struct {
	Code        string   `json:"code"`
	Activity    string   `json:"activity"`
	Completed   int      `json:"completed"`
	OnTime      int      `json:"onTime"`
	Late        int      `json:"late"`
	OnTimePct   float64  `json:"onTimePercent"`
	AvgDaysLate *float64 `json:"avgDaysLate"`
}

// OnTimeTotals is the all-activity roll-up.
type OnTimeTotals struct {
	Completed int     `json:"completed"`
	OnTime    int     `json:"onTime"`
	Late      int     `json:"late"`
	OnTimePct float64 `json:"onTimePercent"`
}

func finishOnTimeRow(r OnTimeRow) OnTimeRow {
	r.Late = r.Completed - r.OnTime
	r.OnTimePct = percent(r.OnTime, r.Completed)
	return r
}

func sumOnTime(rows []OnTimeRow) OnTimeTotals {
	var t OnTimeTotals
	for _, r := range rows {
		t.Completed += r.Completed
		t.OnTime += r.OnTime
	}
	t.Late = t.Completed - t.OnTime
	t.OnTimePct = percent(t.OnTime, t.Completed)
	return t
}

// OnTimeCompletion measures, per activity, how many of those completed in the
// window were finished on or before their scheduled day, and how late the rest
// were on average. The Created activity is excluded (completed at creation).
// A planner can move a scheduled date after the fact, which this report takes
// at face value.
func OnTimeCompletion(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, scope authz.Scope, actor string) ([]OnTimeRow, OnTimeTotals, error) {
	rows := []OnTimeRow{}
	args := []any{}
	owner, ok := calendarScope(ctx, pool, scope, actor, &args)
	if !ok {
		return rows, OnTimeTotals{}, nil
	}
	dates := dateClause("s.completed_at", from, to, &args)
	names := activityNames()
	q := fmt.Sprintf(`
		SELECT s.activity_code, COUNT(*),
		       COUNT(*) FILTER (WHERE s.completed_at::date <= s.scheduled_at::date),
		       AVG((s.completed_at::date - s.scheduled_at::date)::float8) FILTER (WHERE s.completed_at::date > s.scheduled_at::date)
		FROM sales_order_schedule s
		JOIN sales_order so ON so.sales_order_id = s.sales_order_id
		JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		WHERE s.schedule_status = 'completed' AND s.activity_code <> '%s'
		  AND so.sales_order_deleted_at IS NULL AND rs.record_status_code <> '%s'%s%s
		GROUP BY s.activity_code, s.activity_sequence
		ORDER BY s.activity_sequence`, activityCreatedCode, cancelledOrderCode, owner, dates)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, OnTimeTotals{}, fmt.Errorf("query on-time completion: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row OnTimeRow
		if err := r.Scan(&row.Code, &row.Completed, &row.OnTime, &row.AvgDaysLate); err != nil {
			return nil, OnTimeTotals{}, fmt.Errorf("scan on-time completion: %w", err)
		}
		row.Activity = nameOrCode(names, row.Code)
		rows = append(rows, finishOnTimeRow(row))
	}
	if err := r.Err(); err != nil {
		return nil, OnTimeTotals{}, fmt.Errorf("query on-time completion: %w", err)
	}
	return rows, sumOnTime(rows), nil
}

// LeadTimeRow is one month's order-to-install lead time.
type LeadTimeRow struct {
	Period string  `json:"period"` // YYYY-MM-DD, first day of the month
	Orders int     `json:"orders"`
	AvgDay float64 `json:"avgDays"`
	Median float64 `json:"medianDays"`
	MinDay int     `json:"minDays"`
	MaxDay int     `json:"maxDays"`
}

// LeadTimeTotals summarises the whole window.
type LeadTimeTotals struct {
	Orders int     `json:"orders"`
	AvgDay float64 `json:"avgDays"`
	MinDay int     `json:"minDays"`
	MaxDay int     `json:"maxDays"`
}

// sumLeadTime weights each month's average by its order count, so the overall
// average is exact; min and max span every month.
func sumLeadTime(rows []LeadTimeRow) LeadTimeTotals {
	var t LeadTimeTotals
	var weighted float64
	for i, r := range rows {
		t.Orders += r.Orders
		weighted += r.AvgDay * float64(r.Orders)
		if i == 0 || r.MinDay < t.MinDay {
			t.MinDay = r.MinDay
		}
		if r.MaxDay > t.MaxDay {
			t.MaxDay = r.MaxDay
		}
	}
	if t.Orders > 0 {
		t.AvgDay = weighted / float64(t.Orders)
	}
	return t
}

// OrderToInstallLeadTime averages the days from an order's date to the day its
// install was completed, per month of completion. An order's install is its
// first completed install appointment, or its completed Installation activity
// when it has no appointment.
func OrderToInstallLeadTime(ctx context.Context, pool *pgxpool.Pool, from, to time.Time, scope authz.Scope, actor string) ([]LeadTimeRow, LeadTimeTotals, error) {
	rows := []LeadTimeRow{}
	args := []any{}
	owner, ok := calendarScope(ctx, pool, scope, actor, &args)
	if !ok {
		return rows, LeadTimeTotals{}, nil
	}
	dates := dateClause("inst.done_at", from, to, &args)
	q := fmt.Sprintf(`
		WITH inst AS (
			SELECT so.sales_order_id, so.sales_order_date, MIN(x.done_at) AS done_at
			FROM (
				SELECT fj.sales_order_id, ia.completed_at::date AS done_at
				FROM install_appointment ia
				JOIN fabrication_job fj ON fj.fabrication_job_id = ia.fabrication_job_id
				WHERE ia.visit_type = 'install' AND ia.appointment_status = 'completed'
				UNION ALL
				SELECT sch.sales_order_id, sch.completed_at::date
				FROM sales_order_schedule sch
				WHERE sch.activity_code = '%[1]s' AND sch.schedule_status = 'completed'
			) x
			JOIN sales_order so ON so.sales_order_id = x.sales_order_id
			JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
			WHERE so.sales_order_deleted_at IS NULL AND rs.record_status_code <> '%[2]s'%[3]s
			GROUP BY so.sales_order_id, so.sales_order_date
		)
		SELECT to_char(date_trunc('month', inst.done_at), 'YYYY-MM-DD'), COUNT(*),
		       AVG((inst.done_at - inst.sales_order_date)::float8),
		       percentile_cont(0.5) WITHIN GROUP (ORDER BY (inst.done_at - inst.sales_order_date)),
		       MIN(inst.done_at - inst.sales_order_date), MAX(inst.done_at - inst.sales_order_date)
		FROM inst
		WHERE inst.done_at >= inst.sales_order_date%[4]s
		GROUP BY 1
		ORDER BY 1`, salesorder.ActivityInstallation, cancelledOrderCode, owner, strings.ReplaceAll(dates, "\n", " "))
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, LeadTimeTotals{}, fmt.Errorf("query lead time: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row LeadTimeRow
		if err := r.Scan(&row.Period, &row.Orders, &row.AvgDay, &row.Median, &row.MinDay, &row.MaxDay); err != nil {
			return nil, LeadTimeTotals{}, fmt.Errorf("scan lead time: %w", err)
		}
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, LeadTimeTotals{}, fmt.Errorf("query lead time: %w", err)
	}
	return rows, sumLeadTime(rows), nil
}
