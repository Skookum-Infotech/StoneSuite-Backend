//go:build dbtest

package salesorder

import (
	"context"
	"errors"
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
		"template_scheduled":     "2026-10-13T09:00:00",
		"saw_in_progress":        "2026-10-27T09:00:00",
		"installation_picked":    "2026-11-07T09:00:00",
		"installation_completed": "2026-11-10T17:00:00",
	} {
		got := byCode[code]
		if got.ScheduledAt != want || got.Status != ScheduleStatusScheduled || got.CompletedAt != "" {
			t.Errorf("%s = %+v, want scheduled at %s and not completed", code, got, want)
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

	if _, err := UpdateActivity(ctx, pool, o.ID, "template_in_progress",
		ActivityPatch{Status: strp("completed"), Notes: strp("done early")}, 1); err != nil {
		t.Fatalf("UpdateActivity complete: %v", err)
	}
	if _, err := UpdateActivity(ctx, pool, o.ID, "saw_pending",
		ActivityPatch{ScheduledAt: strp("2026-10-30T14:30")}, 1); err != nil {
		t.Fatalf("UpdateActivity reschedule: %v", err)
	}

	if _, err := Update(ctx, pool, o.ID, UpdateOrderInput{
		orderFields: orderFields{InstallDate: "2026-11-10", Memo: "unrelated edit", Items: items},
	}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got := scheduleByCode(t, pool, o.ID)
	if a := got["template_in_progress"]; a.Status != ScheduleStatusCompleted || a.Notes != "done early" {
		t.Errorf("template_in_progress = %+v, want the planner's completion kept", a)
	}
	if a := got["saw_pending"]; a.ScheduledAt != "2026-10-30T14:30:00" {
		t.Errorf("saw_pending scheduled at %s, want the planner's 2026-10-30T14:30:00 kept", a.ScheduledAt)
	}
}

func TestUpdate_ChangedDateReplansOnlyTheOpenActivities(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, items := plannedOrder(t, pool, "2026-11-10")

	before := scheduleByCode(t, pool, o.ID)
	for _, code := range []string{"template_scheduled", "template_in_progress"} {
		if _, err := UpdateActivity(ctx, pool, o.ID, code, ActivityPatch{Status: strp("completed")}, 1); err != nil {
			t.Fatalf("complete %s: %v", code, err)
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
	for _, code := range []string{"created", "template_scheduled", "template_in_progress"} {
		if got[code].ScheduledAt != doneBefore[code].ScheduledAt || got[code].CompletedAt != doneBefore[code].CompletedAt {
			t.Errorf("%s moved on re-plan: %+v -> %+v", code, doneBefore[code], got[code])
		}
	}
	if a := got["installation_completed"]; a.ScheduledAt != "2026-12-01T17:00:00" {
		t.Errorf("installation_completed = %s, want 2026-12-01T17:00:00", a.ScheduledAt)
	}
	if a := got["template_completed"]; a.ScheduledAt == before["template_completed"].ScheduledAt {
		t.Errorf("template_completed did not move on re-plan (%s)", a.ScheduledAt)
	}
	// Nothing open is planned before today (the frozen 2026-10-20).
	for code, a := range got {
		if a.Status == ScheduleStatusScheduled && a.ScheduledAt < "2026-10-20T00:00:00" {
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

func TestUpdateActivity_CompleteBackdateAndRevert(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, _ := plannedOrder(t, pool, "2026-11-10")

	done, err := UpdateActivity(ctx, pool, o.ID, "template_scheduled", ActivityPatch{Status: strp("completed")}, 1)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Status != ScheduleStatusCompleted || done.CompletedAt != "2026-10-12T12:00:00" {
		t.Errorf("completed = %+v, want completed at the (frozen) now", done)
	}

	backdated, err := UpdateActivity(ctx, pool, o.ID, "template_scheduled",
		ActivityPatch{CompletedAt: strp("2026-10-10T08:15")}, 1)
	if err != nil {
		t.Fatalf("backdate: %v", err)
	}
	if backdated.CompletedAt != "2026-10-10T08:15:00" {
		t.Errorf("CompletedAt = %s, want 2026-10-10T08:15:00", backdated.CompletedAt)
	}

	reverted, err := UpdateActivity(ctx, pool, o.ID, "template_scheduled", ActivityPatch{Status: strp("scheduled")}, 1)
	if err != nil {
		t.Fatalf("revert: %v", err)
	}
	if reverted.Status != ScheduleStatusScheduled || reverted.CompletedAt != "" {
		t.Errorf("reverted = %+v, want scheduled with no completion", reverted)
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
		{"empty patch", "saw_pending", ActivityPatch{}, "Nothing to update"},
		{"unknown status", "saw_pending", ActivityPatch{Status: strp("in_progress")}, "scheduled or completed"},
		{"created stays completed", "created", ActivityPatch{Status: strp("scheduled")}, "recorded automatically"},
		{"future completion", "saw_pending", ActivityPatch{Status: strp("completed"), CompletedAt: strp("2026-12-25T09:00")}, "future"},
		{"completed-on without completion", "saw_pending", ActivityPatch{CompletedAt: strp("2026-10-11T09:00")}, "completed activity"},
		{"bad schedule time", "saw_pending", ActivityPatch{ScheduledAt: strp("next tuesday")}, "yyyy-mm-ddThh:mm"},
		{"notes too long", "saw_pending", ActivityPatch{Notes: strp(strings.Repeat("x", scheduleNotesMaxRunes+1))}, "at most"},
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
	if _, err := UpdateActivity(ctx, pool, "00000000-0000-0000-0000-000000000000", "saw_pending", ActivityPatch{Notes: strp("x")}, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown order: err = %v, want ErrNotFound", err)
	}
}

func TestUpdateActivity_NotesAreTrimmedAndSurviveCompletion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	freezeScheduleClock(t, "2026-10-12T12:00")
	o, _ := plannedOrder(t, pool, "2026-11-10")

	if _, err := UpdateActivity(ctx, pool, o.ID, "edges_pending", ActivityPatch{Notes: strp("  waiting on polish pads \n")}, 1); err != nil {
		t.Fatalf("notes: %v", err)
	}
	done, err := UpdateActivity(ctx, pool, o.ID, "edges_pending", ActivityPatch{Status: strp("completed")}, 1)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if done.Notes != "waiting on polish pads" {
		t.Errorf("Notes = %q, want the trimmed note kept through completion", done.Notes)
	}
}
