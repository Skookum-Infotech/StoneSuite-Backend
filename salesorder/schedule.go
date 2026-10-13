package salesorder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// The auto-planned production schedule. Setting an order's scheduled
// installation date generates one sales_order_schedule row per activity below,
// spread evenly from the order date to the installation date; planners then
// move rows through pending, in progress and completed, nudge their dates, and
// add notes.
//
// An activity is a piece of work (Template, Saw, Edges ...), never a stage of
// it: "pending", "in progress" and "completed" are the row's status, so they
// are not repeated in activity names.

// ErrActivityNotFound is returned when an order has no schedule row for the
// requested activity code (an unknown code, or no installation date set yet).
var ErrActivityNotFound = errors.New("schedule activity not found")

// Activity codes the planner treats specially.
const (
	ActivityCreated      = "created"
	ActivityInstallation = "installation"
)

// Row statuses. "Overdue" is not stored: a client derives it from a scheduled
// time in the past on a row that is still 'pending' (a row someone has started
// is not late, and a completed one is done).
const (
	ScheduleStatusPending    = "pending"
	ScheduleStatusInProgress = "in_progress"
	ScheduleStatusCompleted  = "completed"
)

const (
	scheduleDateLayout = "2006-01-02"
	// scheduleStartHour is the wall-clock hour every planned activity starts at.
	scheduleStartHour = 9
	// installDateGraceDays lets a caller whose local "today" is still yesterday
	// in UTC (the Americas, in the evening) pick today without a false "past".
	installDateGraceDays = 1
	// scheduleNotesMaxRunes bounds a planner's note on one activity.
	scheduleNotesMaxRunes = 2000
)

// wallClockLayouts are the zone-less date-times the schedule API accepts.
var wallClockLayouts = []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02 15:04"}

// scheduleClock is the "now" the schedule logic uses; a test seam.
var scheduleClock = func() time.Time { return time.Now().UTC() }

type scheduleActivityDef struct {
	Code  string
	Group string
	Name  string
}

// scheduleCatalog lists every activity in plan order. The slice order is both
// the display order and the order dates are handed out in, and a row's stored
// activity_sequence is its 1-based position here.
var scheduleCatalog = []scheduleActivityDef{
	{ActivityCreated, "Created", "Created"},
	{"template", "Template", "Template"},
	{"material_allocation", "Material Allocation", "Material Allocation"},
	{"engineering", "Engineering", "Engineering"},
	{"wip_bin_transfer", "WIP Bin Transfer", "WIP Bin Transfer"},
	{"saw", "Saw", "Saw"},
	{"edges", "Edges", "Edges"},
	{"quality_control", "Quality Control", "Quality Control"},
	{"installation_ready", "Installation", "Installation Ready"},
	{"installation_picked", "Installation", "Installation Picked"},
	{ActivityInstallation, "Installation", "Installation"},
}

// scheduleActivityByCode indexes scheduleCatalog by code.
func scheduleActivityByCode(code string) (def scheduleActivityDef, sequence int, ok bool) {
	for i, d := range scheduleCatalog {
		if d.Code == code {
			return d, i + 1, true
		}
	}
	return scheduleActivityDef{}, 0, false
}

// ----- pure planning -------------------------------------------------------

// dateOnly truncates t to midnight UTC of its calendar date, discarding any
// time of day (the schedule works in whole calendar days).
func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

type plannedActivity struct {
	code string
	at   time.Time // wall-clock, expressed in UTC by convention
}

// planSchedule hands out a date-time to each code, in order, spreading them
// evenly across the whole days from start up to installDate: with m codes, the
// j-th (1-based) lands round(j × span / m) days after start, so the last lands
// on the installation date and a window shorter than m days simply shares days.
// Installation is always pinned to the installation date itself (the rows
// before it still spread, so Ready and Picked sit just ahead of it). Every
// activity starts at 09:00.
//
// A start after installDate is capped to it, so nothing is ever planned past
// the installation date.
func planSchedule(codes []string, start, installDate time.Time) []plannedActivity {
	start, install := dateOnly(start), dateOnly(installDate)
	if start.After(install) {
		start = install
	}
	span := int(install.Sub(start).Hours() / 24)
	m := len(codes)
	out := make([]plannedActivity, 0, m)
	for j, code := range codes {
		// Round-half-up of (j+1)×span/m using integers only.
		day := (2*(j+1)*span + m) / (2 * m)
		d := start.AddDate(0, 0, day)
		if code == ActivityInstallation {
			d = install
		}
		out = append(out, plannedActivity{code: code, at: d.Add(scheduleStartHour * time.Hour)})
	}
	return out
}

// validateInstallDate parses a scheduled installation date ("yyyy-mm-dd") and
// refuses one in the past, allowing installDateGraceDays of timezone slack.
func validateInstallDate(raw string, today time.Time) (time.Time, error) {
	d, err := time.Parse(scheduleDateLayout, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}, ClientError{Msg: "Scheduled installation date must be in yyyy-mm-dd format."}
	}
	if d.Before(dateOnly(today).AddDate(0, 0, -installDateGraceDays)) {
		return time.Time{}, ClientError{Msg: "Scheduled installation date cannot be in the past."}
	}
	return d, nil
}

// planOrderDate is the order date a plan starts from: the request's value, or
// today when blank / "now" (matching how the sales_order_date column is written).
func planOrderDate(raw string, today time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "now" {
		return dateOnly(today)
	}
	if d, err := time.Parse(scheduleDateLayout, raw); err == nil {
		return d
	}
	return dateOnly(today)
}

// resolveUpdateInstallDate decides what an Update does with the installation
// date. A blank request leaves the stored date (and its schedule) alone — the
// HTTP layer is what makes the field mandatory. A request equal to the stored
// date is accepted even once that date has slipped into the past, so an old
// order stays editable; only a different date is checked and re-plans.
func resolveUpdateInstallDate(requested, stored string, today time.Time) (date time.Time, set, changed bool, err error) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return time.Time{}, false, false, nil
	}
	if requested == stored {
		d, perr := time.Parse(scheduleDateLayout, requested)
		if perr != nil {
			return time.Time{}, false, false, ClientError{Msg: "Scheduled installation date must be in yyyy-mm-dd format."}
		}
		return d, true, false, nil
	}
	d, verr := validateInstallDate(requested, today)
	if verr != nil {
		return time.Time{}, false, false, verr
	}
	return d, true, true, nil
}

// parseWallClock reads a zone-less "yyyy-mm-ddThh:mm[:ss]" date-time.
func parseWallClock(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	for _, layout := range wallClockLayouts {
		if t, err := time.Parse(layout, raw); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised date-time %q", raw)
}

// ----- generation (inside the Create/Update transaction) ---------------------

const createdActivitySQL = `
	INSERT INTO sales_order_schedule
		(sales_order_id, activity_code, activity_sequence, schedule_status,
		 scheduled_at, completed_at, completed_by, schedule_updated_by)
	SELECT so.sales_order_id, $2::text, $3::smallint, 'completed',
	       so.sales_order_created_at, so.sales_order_created_at, $4::int, $4::int
	FROM sales_order so WHERE so.sales_order_id = $1
	ON CONFLICT (sales_order_id, activity_code) DO NOTHING`

const upsertActivitySQL = `
	INSERT INTO sales_order_schedule
		(sales_order_id, activity_code, activity_sequence, scheduled_at, schedule_updated_by)
	VALUES ($1, $2, $3, $4, $5)
	ON CONFLICT (sales_order_id, activity_code) DO UPDATE
	SET scheduled_at = EXCLUDED.scheduled_at,
	    activity_sequence = EXCLUDED.activity_sequence,
	    schedule_updated_at = CURRENT_TIMESTAMP,
	    schedule_updated_by = EXCLUDED.schedule_updated_by
	WHERE sales_order_schedule.schedule_status = 'pending'`

// syncSchedule brings an order's schedule in line with its installation date,
// inside the caller's transaction. It plans when replan is set (the date was
// just entered or changed) or the order has no schedule yet; otherwise it
// leaves the rows alone, so a planner's edits survive an unrelated order save.
// Activities that are in progress or completed are never moved -- the work
// already began at its slot -- and the pending ones are spread from the later
// of the order date and today up to the installation date.
func syncSchedule(ctx context.Context, tx pgx.Tx, orderID int, orderDate, installDate, today time.Time, replan bool, actorEmployeeID int) error {
	rows, err := tx.Query(ctx,
		`SELECT activity_code, schedule_status FROM sales_order_schedule WHERE sales_order_id = $1`, orderID)
	if err != nil {
		return fmt.Errorf("load sales order schedule: %w", err)
	}
	started := map[string]bool{}
	existing := 0
	for rows.Next() {
		var code, status string
		if err := rows.Scan(&code, &status); err != nil {
			rows.Close()
			return fmt.Errorf("scan sales order schedule: %w", err)
		}
		existing++
		if status != ScheduleStatusPending {
			started[code] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read sales order schedule: %w", err)
	}
	if existing > 0 && !replan {
		return nil
	}

	// 'Created' is recorded (at the order's own creation time), never planned.
	if _, err := tx.Exec(ctx, createdActivitySQL,
		orderID, ActivityCreated, 1, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("record created activity: %w", err)
	}

	var codes []string
	for _, def := range scheduleCatalog {
		if def.Code == ActivityCreated || started[def.Code] {
			continue
		}
		codes = append(codes, def.Code)
	}
	if len(codes) == 0 {
		return nil
	}
	start := orderDate
	if today.After(start) {
		start = today
	}
	batch := &pgx.Batch{}
	for _, p := range planSchedule(codes, start, installDate) {
		_, seq, _ := scheduleActivityByCode(p.code)
		batch.Queue(upsertActivitySQL, orderID, p.code, seq, p.at, nullableInt(actorEmployeeID))
	}
	results := tx.SendBatch(ctx, batch)
	for i := 0; i < batch.Len(); i++ {
		if _, err := results.Exec(); err != nil {
			_ = results.Close()
			return fmt.Errorf("plan sales order schedule: %w", err)
		}
	}
	if err := results.Close(); err != nil {
		return fmt.Errorf("plan sales order schedule: %w", err)
	}
	return nil
}

// ----- reads ---------------------------------------------------------------

// ScheduleActivity is one row of an order's schedule as the API returns it.
// ScheduledAt / CompletedAt are zone-less wall-clock "yyyy-mm-ddThh:mm:ss".
type ScheduleActivity struct {
	Code        string `json:"code"`
	Sequence    int    `json:"sequence"`
	Group       string `json:"group"`
	Name        string `json:"name"`
	Status      string `json:"status"` // pending | in_progress | completed
	ScheduledAt string `json:"scheduledAt"`
	CompletedAt string `json:"completedAt,omitempty"`
	Notes       string `json:"notes"`
}

const scheduleSelect = `
	SELECT s.activity_code, s.activity_sequence, s.schedule_status,
	       to_char(s.scheduled_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
	       COALESCE(to_char(s.completed_at, 'YYYY-MM-DD"T"HH24:MI:SS'), ''),
	       s.schedule_notes
	FROM sales_order_schedule s
	WHERE s.sales_order_id = $1
	ORDER BY s.activity_sequence`

// loadSchedule reads an order's schedule rows by internal id.
func loadSchedule(ctx context.Context, q workflow.Querier, orderID int) ([]ScheduleActivity, error) {
	rows, err := q.Query(ctx, scheduleSelect, orderID)
	if err != nil {
		return nil, fmt.Errorf("load sales order schedule: %w", err)
	}
	defer rows.Close()
	out := []ScheduleActivity{}
	for rows.Next() {
		var a ScheduleActivity
		var seq int16
		if err := rows.Scan(&a.Code, &seq, &a.Status, &a.ScheduledAt, &a.CompletedAt, &a.Notes); err != nil {
			return nil, fmt.Errorf("scan sales order schedule: %w", err)
		}
		a.Sequence = int(seq)
		if def, _, ok := scheduleActivityByCode(a.Code); ok {
			a.Group, a.Name = def.Group, def.Name
		} else {
			a.Group, a.Name = a.Code, a.Code
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Schedule returns a live order's planned activities in plan order — empty
// until an installation date has been set.
func Schedule(ctx context.Context, q workflow.Querier, orderUUID string) ([]ScheduleActivity, error) {
	var orderID int
	err := q.QueryRow(ctx,
		`SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL`,
		orderUUID).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve sales order id: %w", err)
	}
	return loadSchedule(ctx, q, orderID)
}

// ----- planner edits ---------------------------------------------------------

// ActivityPatch is a partial update to one schedule row; nil fields are left
// as they are.
type ActivityPatch struct {
	Status      *string `json:"status"`      // pending | in_progress | completed
	ScheduledAt *string `json:"scheduledAt"` // "yyyy-mm-ddThh:mm"
	CompletedAt *string `json:"completedAt"` // only with status completed; defaults to now
	Notes       *string `json:"notes"`
}

// UpdateActivity applies a planner's edit to one row of an order's schedule and
// returns the row as stored. Marking a row completed stamps who and when
// (completedAt defaults to now and may be backdated, never future-dated);
// moving it back to pending or in progress clears both. The Created row is
// recorded by the system, so its status is fixed — its date and notes can still
// be edited.
//
// Unlike Update, this is allowed on a filled or cancelled order: finishing the
// schedule bookkeeping should not be blocked by the order having closed.
func UpdateActivity(ctx context.Context, pool *pgxpool.Pool, orderUUID, code string, p ActivityPatch, actorEmployeeID int) (*ScheduleActivity, error) {
	if p.Status == nil && p.ScheduledAt == nil && p.CompletedAt == nil && p.Notes == nil {
		return nil, ClientError{Msg: "Nothing to update."}
	}
	now := scheduleClock()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update schedule activity: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var orderID int
	err = tx.QueryRow(ctx,
		`SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL`,
		orderUUID).Scan(&orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve sales order id: %w", err)
	}

	var (
		status      string
		scheduledAt time.Time
		completedAt *time.Time
		completedBy *int
		notes       string
	)
	err = tx.QueryRow(ctx, `
		SELECT schedule_status, scheduled_at, completed_at, completed_by, schedule_notes
		FROM sales_order_schedule
		WHERE sales_order_id = $1 AND activity_code = $2
		FOR UPDATE`, orderID, code).Scan(&status, &scheduledAt, &completedAt, &completedBy, &notes)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrActivityNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load schedule activity: %w", err)
	}

	newStatus := status
	if p.Status != nil {
		newStatus = strings.TrimSpace(*p.Status)
		switch newStatus {
		case ScheduleStatusPending, ScheduleStatusInProgress, ScheduleStatusCompleted:
		default:
			return nil, ClientError{Msg: "Status must be pending, in progress or completed."}
		}
		if code == ActivityCreated && newStatus != status {
			return nil, ClientError{Msg: "The Created activity is recorded automatically and cannot be changed."}
		}
	}
	if p.ScheduledAt != nil {
		t, perr := parseWallClock(*p.ScheduledAt)
		if perr != nil {
			return nil, ClientError{Msg: "Scheduled time must look like yyyy-mm-ddThh:mm."}
		}
		scheduledAt = t
	}

	switch newStatus {
	case ScheduleStatusCompleted:
		if p.CompletedAt != nil {
			t, perr := parseWallClock(*p.CompletedAt)
			if perr != nil {
				return nil, ClientError{Msg: "Completed on must look like yyyy-mm-ddThh:mm."}
			}
			completedAt = &t
		} else if status != ScheduleStatusCompleted || completedAt == nil {
			n := now
			completedAt = &n
		}
		// A day of slack for the same timezone reason as the installation date.
		if completedAt.After(now.AddDate(0, 0, installDateGraceDays)) {
			return nil, ClientError{Msg: "Completed on cannot be in the future."}
		}
		if status != ScheduleStatusCompleted {
			completedBy = nullableIntPtr(actorEmployeeID)
		}
	default:
		if p.CompletedAt != nil && strings.TrimSpace(*p.CompletedAt) != "" {
			return nil, ClientError{Msg: "Completed on can only be set on a completed activity."}
		}
		completedAt, completedBy = nil, nil
	}

	if p.Notes != nil {
		notes = strings.TrimSpace(*p.Notes)
		if utf8.RuneCountInString(notes) > scheduleNotesMaxRunes {
			return nil, ClientError{Msg: fmt.Sprintf("Notes can be at most %d characters.", scheduleNotesMaxRunes)}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sales_order_schedule
		SET schedule_status = $3, scheduled_at = $4, completed_at = $5, completed_by = $6,
		    schedule_notes = $7, schedule_updated_at = CURRENT_TIMESTAMP, schedule_updated_by = $8
		WHERE sales_order_id = $1 AND activity_code = $2`,
		orderID, code, newStatus, scheduledAt, completedAt, completedBy, notes, nullableInt(actorEmployeeID),
	); err != nil {
		return nil, fmt.Errorf("update schedule activity: %w", err)
	}

	all, err := loadSchedule(ctx, tx, orderID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update schedule activity: %w", err)
	}
	for i := range all {
		if all[i].Code == code {
			return &all[i], nil
		}
	}
	return nil, ErrActivityNotFound
}

// nullableIntPtr is nullableInt for a typed *int column value: nil for a
// non-positive id.
func nullableIntPtr(v int) *int {
	if v <= 0 {
		return nil
	}
	return &v
}
