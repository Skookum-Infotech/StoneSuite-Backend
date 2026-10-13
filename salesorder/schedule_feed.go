package salesorder

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

// The cross-order view of the production schedule. The calendar lays out one
// activity group (Template, Saw, ...) across a window of dates for every order
// the caller may see, reading the same sales_order_schedule rows that each
// order's own Scheduling tab edits.

const (
	// feedMaxWindowDays is the longest window (inclusive calendar days) one
	// request may ask for -- about a quarter, so a calendar view can never pull a
	// whole year of rows in a single call.
	feedMaxWindowDays = 93
	// feedRowLimit caps the events one request returns; ScheduleFeed reports
	// when the cap cut the list short.
	feedRowLimit = 5000
	// cancelledStatusCode is the order status whose schedule a calendar leaves out.
	cancelledStatusCode = "CANC"
)

// ScheduleEventOrder is the part of an order a calendar card shows.
type ScheduleEventOrder struct {
	ID           string `json:"id"`
	Number       string `json:"number"`
	CustomerName string `json:"customerName"`
	Status       string `json:"status"`     // label, e.g. "Open"
	StatusCode   string `json:"statusCode"` // e.g. "OPEN"
	PONumber     string `json:"poNumber,omitempty"`
	InstallDate  string `json:"scheduledInstallDate,omitempty"`
}

// ScheduleEvent is one planned activity of one order, as the calendar lists it:
// the schedule row itself plus the order it belongs to.
type ScheduleEvent struct {
	ScheduleActivity
	Order ScheduleEventOrder `json:"order"`
}

// ScheduleFeedResult is one window of the feed. Truncated is set when more
// events matched than feedRowLimit allows.
type ScheduleFeedResult struct {
	Events    []ScheduleEvent
	Truncated bool
}

// scheduleGroupCodes lists the activity codes of one catalog group in plan
// order, or nil when no activity belongs to it.
func scheduleGroupCodes(group string) []string {
	var codes []string
	for _, d := range scheduleCatalog {
		if d.Group == group {
			codes = append(codes, d.Code)
		}
	}
	return codes
}

// feedWindow parses a calendar window made of two inclusive "yyyy-mm-dd" dates.
func feedWindow(from, to string) (time.Time, time.Time, error) {
	start, errFrom := time.Parse(scheduleDateLayout, strings.TrimSpace(from))
	end, errTo := time.Parse(scheduleDateLayout, strings.TrimSpace(to))
	if errFrom != nil || errTo != nil {
		return time.Time{}, time.Time{}, ClientError{Msg: "from and to must be dates in yyyy-mm-dd format."}
	}
	if end.Before(start) {
		return time.Time{}, time.Time{}, ClientError{Msg: "to cannot be before from."}
	}
	if days := int(end.Sub(start).Hours()/24) + 1; days > feedMaxWindowDays {
		return time.Time{}, time.Time{}, ClientError{
			Msg: fmt.Sprintf("The date range can span at most %d days.", feedMaxWindowDays),
		}
	}
	return start, end, nil
}

const scheduleFeedSelect = `
	SELECT so.sales_order_uuid, COALESCE(so.sales_order_number,''),
	       COALESCE(c.customer_name,''), rs.record_status_name, rs.record_status_code,
	       COALESCE(so.sales_order_po_number,''),
	       COALESCE(to_char(so.sales_order_scheduled_install_date,'YYYY-MM-DD'),''),
	       s.activity_code, s.activity_sequence, s.schedule_status,
	       to_char(s.scheduled_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
	       COALESCE(to_char(s.completed_at, 'YYYY-MM-DD"T"HH24:MI:SS'), ''),
	       s.schedule_notes
	FROM sales_order_schedule s
	JOIN sales_order so ON so.sales_order_id = s.sales_order_id
	JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
	JOIN customer c ON c.customer_id = so.sales_order_customer_id`

// ScheduleFeed returns every planned activity of one group whose scheduled time
// falls inside the window (from and to are both inclusive), across the orders
// the caller's RBAC scope allows, soonest first. Cancelled and deleted orders
// are left out. A completed activity is listed on the day it was *scheduled*
// for, so a plan keeps its shape once the work is done.
func ScheduleFeed(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID, group, from, to string) (ScheduleFeedResult, error) {
	codes := scheduleGroupCodes(strings.TrimSpace(group))
	if len(codes) == 0 {
		return ScheduleFeedResult{}, ClientError{Msg: "Unknown activity group."}
	}
	start, end, err := feedWindow(from, to)
	if err != nil {
		return ScheduleFeedResult{}, err
	}

	out := ScheduleFeedResult{Events: []ScheduleEvent{}}
	where := []string{
		"so.sales_order_deleted_at IS NULL",
		"rs.record_status_code <> $4",
		"s.activity_code = ANY($1)",
		"s.scheduled_at >= $2::date",
		"s.scheduled_at < $3::date + 1",
	}
	args := []any{codes, start.Format(scheduleDateLayout), end.Format(scheduleDateLayout), cancelledStatusCode}
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return out, nil
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("so.sales_order_owner_id = $%d", len(args)))
	}

	q := scheduleFeedSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY s.scheduled_at, so.sales_order_number, s.activity_sequence" +
		fmt.Sprintf(" LIMIT %d", feedRowLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return ScheduleFeedResult{}, fmt.Errorf("load schedule calendar: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var e ScheduleEvent
		var seq int16
		if err := rows.Scan(
			&e.Order.ID, &e.Order.Number, &e.Order.CustomerName, &e.Order.Status, &e.Order.StatusCode,
			&e.Order.PONumber, &e.Order.InstallDate,
			&e.Code, &seq, &e.Status, &e.ScheduledAt, &e.CompletedAt, &e.Notes,
		); err != nil {
			return ScheduleFeedResult{}, fmt.Errorf("scan schedule calendar: %w", err)
		}
		e.Sequence = int(seq)
		if def, _, ok := scheduleActivityByCode(e.Code); ok {
			e.Group, e.Name = def.Group, def.Name
		} else {
			e.Group, e.Name = e.Code, e.Code
		}
		out.Events = append(out.Events, e)
	}
	if err := rows.Err(); err != nil {
		return ScheduleFeedResult{}, fmt.Errorf("read schedule calendar: %w", err)
	}
	if len(out.Events) > feedRowLimit {
		out.Events = out.Events[:feedRowLimit]
		out.Truncated = true
	}
	return out, nil
}
