//go:build dbtest

package salesorder

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// freezeScheduleClock pins "now" for the schedule logic so planned dates are
// deterministic whatever day the suite runs on.
func freezeScheduleClock(t *testing.T, at string) {
	t.Helper()
	frozen, err := time.Parse("2006-01-02T15:04", at)
	if err != nil {
		t.Fatalf("bad frozen time %q: %v", at, err)
	}
	prev := scheduleClock
	scheduleClock = func() time.Time { return frozen }
	t.Cleanup(func() { scheduleClock = prev })
}

// plannedOrder creates an order with the given installation date (blank for
// none) and returns it together with the customer and item it was made from,
// so a test can save it again.
func plannedOrder(t *testing.T, pool *pgxpool.Pool, installDate string) (*Order, []LineInput2) {
	t.Helper()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	items := []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	o, err := Create(context.Background(), pool, CreateOrderInput{
		CustomerUUID: custUUID,
		orderFields:  orderFields{InstallDate: installDate, Items: items},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return o, items
}

func scheduleByCode(t *testing.T, pool *pgxpool.Pool, orderUUID string) map[string]ScheduleActivity {
	t.Helper()
	acts, err := Schedule(context.Background(), pool, orderUUID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	out := make(map[string]ScheduleActivity, len(acts))
	for _, a := range acts {
		out[a.Code] = a
	}
	return out
}

func strp(s string) *string { return &s }

func TestCreate_PlansTheScheduleFromTheInstallDate(t *testing.T) {
	pool := testPool(t)
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, _ := plannedOrder(t, pool, "2026-11-10")

	if o.InstallDate != "2026-11-10" {
		t.Errorf("InstallDate = %q, want 2026-11-10", o.InstallDate)
	}
	acts, err := Schedule(context.Background(), pool, o.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(acts) != len(scheduleCatalog) {
		t.Fatalf("got %d activities, want %d", len(acts), len(scheduleCatalog))
	}
	for i, a := range acts {
		if a.Code != scheduleCatalog[i].Code || a.Sequence != i+1 {
			t.Errorf("row %d = %s (seq %d), want %s (seq %d)", i, a.Code, a.Sequence, scheduleCatalog[i].Code, i+1)
		}
		if a.Name != scheduleCatalog[i].Name || a.Group != scheduleCatalog[i].Group {
			t.Errorf("row %d name/group = %q/%q", i, a.Name, a.Group)
		}
	}
	created := acts[0]
	if created.Status != ScheduleStatusCompleted || created.CompletedAt == "" {
		t.Errorf("Created = %+v, want it recorded as completed", created)
	}
	byCode := scheduleByCode(t, pool, o.ID)
	for code, want := range map[string]string{
		"template":            "2026-10-15T09:00:00",
		"saw":                 "2026-10-27T09:00:00",
		"installation_picked": "2026-11-07T09:00:00",
		"installation":        "2026-11-10T09:00:00",
	} {
		got := byCode[code]
		if got.ScheduledAt != want || got.Status != ScheduleStatusPending || got.CompletedAt != "" {
			t.Errorf("%s = %+v, want pending at %s and not completed", code, got, want)
		}
	}
}

func TestCreate_RefusesAPastInstallDate(t *testing.T) {
	pool := testPool(t)
	freezeScheduleClock(t, "2026-10-12T12:00")
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	_, err := Create(context.Background(), pool, CreateOrderInput{
		CustomerUUID: custUUID,
		orderFields: orderFields{
			InstallDate: "2026-10-01",
			Items:       []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
		},
	}, 1)
	if !IsClientError(err) || !strings.Contains(err.Error(), "past") {
		t.Fatalf("err = %v, want a client error about the past", err)
	}
}

func TestCreate_WithoutAnInstallDatePlansNothing(t *testing.T) {
	pool := testPool(t)
	o, _ := plannedOrder(t, pool, "")
	if o.InstallDate != "" {
		t.Errorf("InstallDate = %q, want blank", o.InstallDate)
	}
	acts, err := Schedule(context.Background(), pool, o.ID)
	if err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	if len(acts) != 0 {
		t.Errorf("got %d schedule rows for an order with no date", len(acts))
	}
}

func TestUpdate_KeepsPlannerEditsWhenTheDateIsUnchanged(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, items := plannedOrder(t, pool, "2026-11-10")

	if _, err := UpdateActivity(ctx, pool, o.ID, "template",
		ActivityPatch{Status: strp("completed"), Notes: strp("done early")}, 1); err != nil {
		t.Fatalf("UpdateActivity complete: %v", err)
	}
	if _, err := UpdateActivity(ctx, pool, o.ID, "saw",
		ActivityPatch{ScheduledAt: strp("2026-10-30T14:30")}, 1); err != nil {
		t.Fatalf("UpdateActivity reschedule: %v", err)
	}

	if _, err := Update(ctx, pool, o.ID, UpdateOrderInput{
		orderFields: orderFields{InstallDate: "2026-11-10", Memo: "unrelated edit", Items: items},
	}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := scheduleByCode(t, pool, o.ID)
	if a := got["template"]; a.Status != ScheduleStatusCompleted || a.Notes != "done early" {
		t.Errorf("template = %+v, want the planner's completion kept", a)
	}
	if a := got["saw"]; a.ScheduledAt != "2026-10-30T14:30:00" {
		t.Errorf("saw scheduled at %s, want the planner's 2026-10-30T14:30:00 kept", a.ScheduledAt)
	}
}

func TestUpdate_ChangedDateReplansOnlyThePendingActivities(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, items := plannedOrder(t, pool, "2026-11-10")

	before := scheduleByCode(t, pool, o.ID)
	// One finished and one started: neither may move when the date changes.
	for code, status := range map[string]string{"template": "completed", "material_allocation": "in_progress"} {
		if _, err := UpdateActivity(ctx, pool, o.ID, code, ActivityPatch{Status: strp(status)}, 1); err != nil {
			t.Fatalf("mark %s %s: %v", code, status, err)
		}
	}
	doneBefore := scheduleByCode(t, pool, o.ID)

	freezeScheduleClock(t, "2026-10-20T12:00")
	updated, err := Update(ctx, pool, o.ID, UpdateOrderInput{
		orderFields: orderFields{InstallDate: "2026-12-01", Items: items},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.InstallDate != "2026-12-01" {
		t.Errorf("InstallDate = %q, want 2026-12-01", updated.InstallDate)
	}

	got := scheduleByCode(t, pool, o.ID)
	for _, code := range []string{"created", "template", "material_allocation"} {
		if got[code].ScheduledAt != doneBefore[code].ScheduledAt || got[code].CompletedAt != doneBefore[code].CompletedAt ||
			got[code].Status != doneBefore[code].Status {
			t.Errorf("%s moved on re-plan: %+v -> %+v", code, doneBefore[code], got[code])
		}
	}
	if a := got["installation"]; a.ScheduledAt != "2026-12-01T09:00:00" {
		t.Errorf("installation = %s, want 2026-12-01T09:00:00", a.ScheduledAt)
	}
	if a := got["engineering"]; a.ScheduledAt == before["engineering"].ScheduledAt {
		t.Errorf("engineering did not move on re-plan (%s)", a.ScheduledAt)
	}
	// Nothing pending is planned before today (the frozen 2026-10-20).
	for code, a := range got {
		if a.Status == ScheduleStatusPending && a.ScheduledAt < "2026-10-20T00:00:00" {
			t.Errorf("%s re-planned into the past: %s", code, a.ScheduledAt)
		}
	}
}

func TestUpdate_BlankDateLeavesTheStoredDateAndSchedule(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, items := plannedOrder(t, pool, "2026-11-10")

	updated, err := Update(ctx, pool, o.ID, UpdateOrderInput{orderFields: orderFields{Items: items}}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.InstallDate != "2026-11-10" {
		t.Errorf("InstallDate = %q, want the stored 2026-11-10 left alone", updated.InstallDate)
	}
	if n := len(scheduleByCode(t, pool, o.ID)); n != len(scheduleCatalog) {
		t.Errorf("schedule has %d rows, want %d", n, len(scheduleCatalog))
	}
}

func TestUpdate_TheStoredDateStaysAcceptableOncePastButANewPastDateIsRefused(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, items := plannedOrder(t, pool, "2026-10-20")

	freezeScheduleClock(t, "2026-11-15T12:00") // the installation date is now long past
	if _, err := Update(ctx, pool, o.ID, UpdateOrderInput{
		orderFields: orderFields{InstallDate: "2026-10-20", Memo: "late edit", Items: items},
	}, 1); err != nil {
		t.Fatalf("saving the unchanged, now-past date must still work: %v", err)
	}
	_, err := Update(ctx, pool, o.ID, UpdateOrderInput{
		orderFields: orderFields{InstallDate: "2026-11-01", Items: items},
	}, 1)
	if !IsClientError(err) || !strings.Contains(err.Error(), "past") {
		t.Fatalf("err = %v, want a client error about the past", err)
	}
}

func TestUpdate_FirstDateOnALegacyOrderPlansItsSchedule(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, items := plannedOrder(t, pool, "")

	if _, err := Update(ctx, pool, o.ID, UpdateOrderInput{
		orderFields: orderFields{InstallDate: "2026-11-10", Items: items},
	}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := scheduleByCode(t, pool, o.ID)
	if len(got) != len(scheduleCatalog) {
		t.Fatalf("got %d rows, want %d", len(got), len(scheduleCatalog))
	}
	if a := got["created"]; a.Status != ScheduleStatusCompleted {
		t.Errorf("created = %+v, want completed", a)
	}
}

func TestUpdateActivity_StartCompleteBackdateAndRevert(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, _ := plannedOrder(t, pool, "2026-11-10")

	started, err := UpdateActivity(ctx, pool, o.ID, "template", ActivityPatch{Status: strp("in_progress")}, 1)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if started.Status != ScheduleStatusInProgress || started.CompletedAt != "" {
		t.Errorf("started = %+v, want in progress with no completion", started)
	}

	done, err := UpdateActivity(ctx, pool, o.ID, "template", ActivityPatch{Status: strp("completed")}, 1)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Status != ScheduleStatusCompleted || done.CompletedAt != "2026-10-12T12:00:00" {
		t.Errorf("completed = %+v, want completed at the (frozen) now", done)
	}

	backdated, err := UpdateActivity(ctx, pool, o.ID, "template",
		ActivityPatch{CompletedAt: strp("2026-10-10T08:15")}, 1)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if backdated.CompletedAt != "2026-10-10T08:15:00" {
		t.Errorf("CompletedAt = %s, want 2026-10-10T08:15:00", backdated.CompletedAt)
	}

	reopened, err := UpdateActivity(ctx, pool, o.ID, "template", ActivityPatch{Status: strp("in_progress")}, 1)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.Status != ScheduleStatusInProgress || reopened.CompletedAt != "" {
		t.Errorf("reopened = %+v, want in progress with no completion", reopened)
	}

	reverted, err := UpdateActivity(ctx, pool, o.ID, "template", ActivityPatch{Status: strp("pending")}, 1)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if reverted.Status != ScheduleStatusPending || reverted.CompletedAt != "" {
		t.Errorf("reverted = %+v, want pending with no completion", reverted)
	}
}

func TestUpdateActivity_Refusals(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, _ := plannedOrder(t, pool, "2026-11-10")

	for _, tc := range []struct {
		name    string
		code    string
		patch   ActivityPatch
		wantErr string
	}{
		{"empty patch", "saw", ActivityPatch{}, "Nothing to update"},
		{"unknown status", "saw", ActivityPatch{Status: strp("scheduled")}, "pending, in progress or completed"},
		{"created stays completed", "created", ActivityPatch{Status: strp("pending")}, "recorded automatically"},
		{"future completion", "saw", ActivityPatch{Status: strp("completed"), CompletedAt: strp("2026-12-25T09:00")}, "future"},
		{"completed-on without completion", "saw", ActivityPatch{CompletedAt: strp("2026-10-11T09:00")}, "completed activity"},
		{"completed-on while in progress", "saw", ActivityPatch{Status: strp("in_progress"), CompletedAt: strp("2026-10-11T09:00")}, "completed activity"},
		{"bad schedule time", "saw", ActivityPatch{ScheduledAt: strp("next tuesday")}, "yyyy-mm-ddThh:mm"},
		{"notes too long", "saw", ActivityPatch{Notes: strp(strings.Repeat("x", scheduleNotesMaxRunes+1))}, "at most"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := UpdateActivity(ctx, pool, o.ID, tc.code, tc.patch, 1)
			if !IsClientError(err) || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want a client error containing %q", err, tc.wantErr)
			}
		})
	}

	if _, err := UpdateActivity(ctx, pool, o.ID, "no_such_activity", ActivityPatch{Notes: strp("x")}, 1); !errors.Is(err, ErrActivityNotFound) {
		t.Errorf("unknown activity: err = %v, want ErrActivityNotFound", err)
	}
	// A code from the retired 23-activity catalog is just as unknown.
	if _, err := UpdateActivity(ctx, pool, o.ID, "saw_pending", ActivityPatch{Notes: strp("x")}, 1); !errors.Is(err, ErrActivityNotFound) {
		t.Errorf("retired activity code: err = %v, want ErrActivityNotFound", err)
	}
	if _, err := UpdateActivity(ctx, pool, "00000000-0000-0000-0000-000000000000", "saw", ActivityPatch{Notes: strp("x")}, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown order: err = %v, want ErrNotFound", err)
	}
}

func TestUpdateActivity_NotesAreTrimmedAndSurviveCompletion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, _ := plannedOrder(t, pool, "2026-11-10")

	if _, err := UpdateActivity(ctx, pool, o.ID, "edges", ActivityPatch{Notes: strp("  waiting on polish pads \n")}, 1); err != nil {
		t.Fatalf("notes: %v", err)
	}
	done, err := UpdateActivity(ctx, pool, o.ID, "edges", ActivityPatch{Status: strp("completed")}, 1)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Notes != "waiting on polish pads" {
		t.Errorf("Notes = %q, want the trimmed note kept through completion", done.Notes)
	}
}

// legacyScheduleCodes is the retired 23-activity catalog in plan order. Row i
// was planned i days after 2026-10-12 09:00.
var legacyScheduleCodes = []string{
	"created",
	"template_scheduled", "template_in_progress", "template_completed",
	"material_allocation_pending", "material_allocated",
	"engineering_pending", "engineering_in_progress", "engineering_completed",
	"wip_bin_transfer",
	"saw_pending", "saw_in_progress", "saw_completed",
	"edges_pending", "edges_in_progress", "edges_completed",
	"quality_control_pending", "quality_control_in_progress", "quality_control_completed",
	"installation_ready", "installation_picked", "installation_in_progress", "installation_completed",
}

// scheduleFoldBlocks returns the two DO blocks of schema.sql that bring a
// pre-split schedule table up to date -- the status vocabulary, then the fold of
// stage rows into activities -- exactly as shipped. They are run on their own
// because re-applying the whole schema over a shared test database trips an
// unrelated, older CHECK (chk_sales_order_history_action) once other suites have
// left 'reject' / 'approve_override' history rows behind.
func scheduleFoldBlocks(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../database/migrations/tenant/schema.sql")
	if err != nil {
		t.Fatalf("read schema.sql: %v", err)
	}
	src := strings.ReplaceAll(string(raw), "\r", "")
	vocabulary := regexp.MustCompile(`(?s)DO \$\$\s*BEGIN\s*ALTER TABLE sales_order_schedule DROP CONSTRAINT.*?END \$\$;`).FindString(src)
	fold := regexp.MustCompile(`(?s)DO \$\$\s*BEGIN\s*UPDATE sales_order_schedule s\b.*?END \$\$;`).FindString(src)
	if vocabulary == "" || fold == "" {
		t.Fatalf("the schedule migration blocks were not found in schema.sql (vocabulary=%t fold=%t)", vocabulary != "", fold != "")
	}
	return vocabulary + "\n" + fold
}

// TestTenantSchema_FoldsLegacyStageRowsIntoActivities replays what an existing
// tenant holds -- a schedule of 23 stage rows with the old 'scheduled' status --
// and checks that the migration folds each phase into one activity, keeping
// status, the planner's dates and notes, and that running it again is a no-op.
func TestTenantSchema_FoldsLegacyStageRowsIntoActivities(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, _ := plannedOrder(t, pool, "2026-11-10")
	blocks := scheduleFoldBlocks(t)

	// Whatever happens below, leave the shared table as the schema defines it.
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), blocks); err != nil {
			t.Errorf("restore schedule schema: %v", err)
		}
	})

	var orderID int
	if err := pool.QueryRow(ctx,
		`SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1`, o.ID).Scan(&orderID); err != nil {
		t.Fatalf("resolve order id: %v", err)
	}

	// Put the table back into its legacy shape: the old status vocabulary is not
	// enforced, and this order holds the 23 stage rows instead of 11 activities.
	if _, err := pool.Exec(ctx, `ALTER TABLE sales_order_schedule DROP CONSTRAINT IF EXISTS chk_so_schedule_status`); err != nil {
		t.Fatalf("drop status constraint: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM sales_order_schedule WHERE sales_order_id = $1`, orderID); err != nil {
		t.Fatalf("clear schedule: %v", err)
	}
	completed := map[string]bool{
		"created": true, "template_scheduled": true, "template_in_progress": true,
		"material_allocation_pending": true,
		"engineering_pending":         true, "engineering_in_progress": true, "engineering_completed": true,
		"wip_bin_transfer": true, "saw_completed": true,
		"installation_picked": true, "installation_in_progress": true,
	}
	notes := map[string]string{"template_scheduled": "booked", "template_in_progress": "on site"}
	for i, code := range legacyScheduleCodes {
		status := "scheduled"
		if completed[code] {
			status = "completed"
		}
		if _, err := pool.Exec(ctx, `
			INSERT INTO sales_order_schedule
				(sales_order_id, activity_code, activity_sequence, schedule_status, scheduled_at, completed_at, schedule_notes)
			VALUES ($1, $2, $3, $4::text,
				TIMESTAMP '2026-10-12 09:00' + make_interval(days => $5::int),
				CASE WHEN $4::text = 'completed' THEN TIMESTAMP '2026-10-12 10:00' + make_interval(days => $5::int) END,
				$6)`,
			orderID, code, i+1, status, i, notes[code]); err != nil {
			t.Fatalf("seed legacy row %s: %v", code, err)
		}
	}

	type want struct {
		seq         int
		status      string
		scheduledAt string
		completedAt string
		notes       string
	}
	expect := map[string]want{
		"created":             {1, ScheduleStatusCompleted, "2026-10-12T09:00:00", "2026-10-12T10:00:00", ""},
		"template":            {2, ScheduleStatusInProgress, "2026-10-13T09:00:00", "", "booked\non site"},
		"material_allocation": {3, ScheduleStatusPending, "2026-10-16T09:00:00", "", ""},
		"engineering":         {4, ScheduleStatusCompleted, "2026-10-18T09:00:00", "2026-10-20T10:00:00", ""},
		"wip_bin_transfer":    {5, ScheduleStatusCompleted, "2026-10-21T09:00:00", "2026-10-21T10:00:00", ""},
		"saw":                 {6, ScheduleStatusCompleted, "2026-10-22T09:00:00", "2026-10-24T10:00:00", ""},
		"edges":               {7, ScheduleStatusPending, "2026-10-25T09:00:00", "", ""},
		"quality_control":     {8, ScheduleStatusPending, "2026-10-28T09:00:00", "", ""},
		"installation_ready":  {9, ScheduleStatusPending, "2026-10-31T09:00:00", "", ""},
		"installation_picked": {10, ScheduleStatusCompleted, "2026-11-01T09:00:00", "2026-11-01T10:00:00", ""},
		"installation":        {11, ScheduleStatusInProgress, "2026-11-02T09:00:00", "", ""},
	}

	// Run it twice: the second run must change nothing.
	for run := 1; run <= 2; run++ {
		if _, err := pool.Exec(ctx, blocks); err != nil {
			t.Fatalf("run the schedule migration (run %d): %v", run, err)
		}
		acts, err := Schedule(ctx, pool, o.ID)
		if err != nil {
			t.Fatalf("Schedule (run %d): %v", run, err)
		}
		if len(acts) != len(scheduleCatalog) {
			t.Fatalf("run %d: got %d activities, want %d", run, len(acts), len(scheduleCatalog))
		}
		for i, a := range acts {
			def := scheduleCatalog[i]
			if a.Code != def.Code || a.Sequence != i+1 || a.Name != def.Name || a.Group != def.Group {
				t.Errorf("run %d row %d = %s %q/%q seq %d, want %s %q/%q seq %d",
					run, i, a.Code, a.Group, a.Name, a.Sequence, def.Code, def.Group, def.Name, i+1)
			}
			w := expect[a.Code]
			if a.Status != w.status || a.ScheduledAt != w.scheduledAt || a.CompletedAt != w.completedAt || a.Notes != w.notes {
				t.Errorf("run %d %s = status %s at %s done %q notes %q, want status %s at %s done %q notes %q",
					run, a.Code, a.Status, a.ScheduledAt, a.CompletedAt, a.Notes,
					w.status, w.scheduledAt, w.completedAt, w.notes)
			}
		}
	}

	// The vocabulary constraint is back, and now allows in_progress.
	var def string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'chk_so_schedule_status' AND conrelid = 'sales_order_schedule'::regclass`).Scan(&def); err != nil {
		t.Fatalf("read status constraint: %v", err)
	}
	if !strings.Contains(def, "in_progress") || strings.Contains(def, "scheduled") {
		t.Errorf("status constraint = %s, want pending / in_progress / completed", def)
	}
}
