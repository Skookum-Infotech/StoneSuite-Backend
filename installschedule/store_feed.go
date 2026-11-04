package installschedule

import (
	"context"
	"fmt"
	"strings"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/dateonly"
)

// feedRowCap is the row limit for Feed; a var so tests can lower it.
var feedRowCap = FeedRowCap

const (
	fieldFrom = "from"
	fieldTo   = "to"
	hoursDay  = 24
)

// FeedRequest is the decoded calendar feed query.
type FeedRequest struct {
	From, To         string
	EmployeeID       int
	VisitType        VisitType
	IncludeCancelled bool
}

// feedWindow converts the local from/to dates to UTC half-open bounds.
func feedWindow(loc *time.Location, req FeedRequest) (time.Time, time.Time, error) {
	from, err := dateonly.Parse(req.From)
	if err != nil {
		return time.Time{}, time.Time{}, &ValidationError{Field: fieldFrom, Message: "must be YYYY-MM-DD"}
	}
	to, err := dateonly.Parse(req.To)
	if err != nil {
		return time.Time{}, time.Time{}, &ValidationError{Field: fieldTo, Message: "must be YYYY-MM-DD"}
	}
	fy, fm, fd := from.Date()
	ty, tm, td := to.Date()
	start := time.Date(fy, fm, fd, 0, 0, 0, 0, loc)
	end := time.Date(ty, tm, td+1, 0, 0, 0, 0, loc)
	if !end.After(start) {
		return time.Time{}, time.Time{}, &ValidationError{Field: fieldTo, Message: "must not be before from"}
	}
	if end.Sub(start) > MaxFeedDays*hoursDay*time.Hour+time.Hour {
		return time.Time{}, time.Time{}, &ValidationError{Field: fieldTo, Message: fmt.Sprintf("window exceeds %d days", MaxFeedDays)}
	}
	return start.UTC(), end.UTC(), nil
}

// Feed lists appointments overlapping the local-date window, narrowed by scope and filters.
func Feed(ctx context.Context, q Querier, loc *time.Location, scope, actorIdentityID string, req FeedRequest) ([]Appointment, bool, error) {
	winStart, winEnd, err := feedWindow(loc, req)
	if err != nil {
		return nil, false, err
	}
	args := []any{winEnd, winStart, winStart.Add(-MaxDuration)}
	where := []string{
		"ia.starts_at < $1", "ia.ends_at > $2", "ia.starts_at >= $3",
		"fj.fabrication_job_deleted_at IS NULL",
	}
	if !req.IncludeCancelled {
		args = append(args, string(StatusCancelled))
		where = append(where, fmt.Sprintf("ia.appointment_status <> $%d", len(args)))
	}
	if req.VisitType != "" {
		args = append(args, string(req.VisitType))
		where = append(where, fmt.Sprintf("ia.visit_type = $%d", len(args)))
	}
	if req.EmployeeID > 0 {
		args = append(args, req.EmployeeID)
		where = append(where, fmt.Sprintf(`EXISTS (SELECT 1 FROM install_appointment_crew f
			WHERE f.install_appointment_id = ia.install_appointment_id AND f.employee_id = $%d)`, len(args)))
	}
	if scope != string(authz.ScopeAll) {
		if actorIdentityID == "" {
			return []Appointment{}, false, nil
		}
		args = append(args, actorIdentityID)
		n := len(args)
		where = append(where, fmt.Sprintf(`(
			EXISTS (SELECT 1 FROM employee oe JOIN users ou ON ou.id = oe.employee_user_id
			        WHERE oe.employee_id = fj.job_owner_id AND ou.identity_id = $%[1]d)
			OR EXISTS (SELECT 1 FROM install_appointment_crew sc
			           JOIN employee se ON se.employee_id = sc.employee_id
			           JOIN users su ON su.id = se.employee_user_id
			           WHERE sc.install_appointment_id = ia.install_appointment_id AND su.identity_id = $%[1]d))`, n))
	}
	args = append(args, feedRowCap+1)
	items, err := collect(ctx, q, apptSelect+" WHERE "+strings.Join(where, " AND ")+
		fmt.Sprintf(" ORDER BY ia.starts_at, ia.install_appointment_id LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, false, fmt.Errorf("feed: %w", err)
	}
	truncated := len(items) > feedRowCap
	if truncated {
		items = items[:feedRowCap]
	}
	return items, truncated, nil
}
