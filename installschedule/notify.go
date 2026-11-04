package installschedule

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// Recipient groups produced by recipients.
const (
	groupAll     = "all"
	groupAdded   = "added"
	groupRemoved = "removed"
)

// Notification formatting constants.
const (
	statusLinkFormat = "/calendar/install-schedule?appointment=%s"
	whenDayLayout    = "Mon 2 Jan"
	whenClockLayout  = "3:04"
	whenZoneLayout   = "MST"
	keySeparator     = ":"
)

// sendEmployees is the seam NotifyChange uses to send; tests replace it.
type sendEmployees func(ctx context.Context, pool *pgxpool.Pool, ev approvalchain.EmployeesEvent) ([]string, error)

// IdempotencyKey is the hex sha256 of uuid, version, event type and the (order-insensitive) recipient set.
func IdempotencyKey(apptUUID string, version int, eventType string, recipients []int) string {
	ids := sortedUniqueIDs(recipients)
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	setHash := sha256.Sum256([]byte(strings.Join(parts, ",")))
	sum := sha256.Sum256([]byte(strings.Join([]string{apptUUID, strconv.Itoa(version), eventType, hex.EncodeToString(setHash[:])}, keySeparator)))
	return hex.EncodeToString(sum[:])
}

// recipients groups the employees to notify for a change, never including the actor.
// "all" gets the generic message, "added" and "removed" the reassign ones. Empty groups are omitted.
func recipients(res *ChangeResult, actorEmployeeID int) map[string][]int {
	var added, removed, all []int
	for _, action := range res.Actions {
		switch action {
		case ActionReassign:
			added, removed = res.AddedCrew, res.RemovedCrew
		case ActionCreate, ActionCancel, ActionReschedule:
			all = append(all, crewOf(res.After)...)
			if action == ActionReschedule {
				all = append(all, crewOf(res.Before)...)
			}
		}
	}
	special := map[int]bool{}
	for _, id := range append(append([]int{}, added...), removed...) {
		special[id] = true
	}
	var generic []int
	for _, id := range sortedUniqueIDs(all) {
		if !special[id] {
			generic = append(generic, id)
		}
	}
	out := map[string][]int{}
	for group, ids := range map[string][]int{groupAll: generic, groupAdded: added, groupRemoved: removed} {
		if kept := withoutActor(sortedUniqueIDs(ids), actorEmployeeID); len(kept) > 0 {
			out[group] = kept
		}
	}
	return out
}

// crewOf returns the employee ids of an appointment's crew.
func crewOf(a *Appointment) []int {
	if a == nil {
		return nil
	}
	ids := make([]int, 0, len(a.Crew))
	for _, c := range a.Crew {
		ids = append(ids, c.EmployeeID)
	}
	return ids
}

// withoutActor drops the actor's employee id from ids.
func withoutActor(ids []int, actor int) []int {
	var out []int
	for _, id := range ids {
		if id != actor {
			out = append(out, id)
		}
	}
	return out
}

// whenText renders the appointment span in its own zone, e.g. "Tue 10 Nov, 8:00-12:00 CST".
func whenText(a *Appointment) string {
	loc, err := time.LoadLocation(a.Timezone)
	if err != nil {
		loc = time.UTC
	}
	start, end := a.StartsAt.In(loc), a.EndsAt.In(loc)
	if a.AllDay {
		return start.Format(whenDayLayout) + " (all day)"
	}
	return fmt.Sprintf("%s, %s–%s %s", start.Format(whenDayLayout), start.Format(whenClockLayout), end.Format(whenClockLayout), start.Format(whenZoneLayout))
}

// messageFor builds the notification title and body for an event type and recipient group.
func messageFor(eventType, group string, a *Appointment) (title, body string) {
	subject := fmt.Sprintf("%s visit for job %s", capitalize(string(a.VisitType)), a.JobNumber)
	when := whenText(a)
	switch group {
	case groupAdded:
		return "You've been booked: " + subject, when
	case groupRemoved:
		return "You've been taken off: " + subject, when
	}
	switch eventType {
	case EventRescheduled:
		return "Rescheduled: " + subject, "Now " + when
	case EventCancelled:
		return "Cancelled: " + subject, "Was " + when
	default:
		return "Scheduled: " + subject, when
	}
}

// NotifyCreate notifies the crew of a newly booked appointment, persists the notify ids on the history row,
// and reports whether any notification was created. It never fails the caller.
func NotifyCreate(ctx context.Context, pool *pgxpool.Pool, res *CreateResult, actorUserID string, actorEmployeeID int) (emailSent bool, ids []string) {
	if res == nil || res.Replayed {
		return false, nil
	}
	cr := &ChangeResult{After: res.Appointment, HistoryIDs: []int64{res.HistoryID}, Actions: []string{ActionCreate}}
	return notifyChange(ctx, pool, cr, actorUserID, actorEmployeeID, sendViaApprovalchain)
}

// NotifyChange notifies the affected crew of an update, cancel or complete (see recipients), persists the
// notify ids on the first history row, and reports whether any notification was created. It never fails the caller.
func NotifyChange(ctx context.Context, pool *pgxpool.Pool, res *ChangeResult, actorUserID string, actorEmployeeID int) (emailSent bool, ids []string) {
	return notifyChange(ctx, pool, res, actorUserID, actorEmployeeID, sendViaApprovalchain)
}

// sendViaApprovalchain adapts approvalchain.NotifyEmployees to the sendEmployees seam.
func sendViaApprovalchain(ctx context.Context, pool *pgxpool.Pool, ev approvalchain.EmployeesEvent) ([]string, error) {
	r, err := approvalchain.NotifyEmployees(ctx, pool, ev)
	return r.NotificationIDs, err
}

// notifyChange is NotifyChange with the sender injected.
func notifyChange(ctx context.Context, pool *pgxpool.Pool, res *ChangeResult, actorUserID string, actorEmployeeID int, send sendEmployees) (bool, []string) {
	if res == nil || res.After == nil {
		return false, nil
	}
	eventType := primaryEventType(res.Actions)
	var ids []string
	groups := recipients(res, actorEmployeeID)
	for _, group := range []string{groupAll, groupAdded, groupRemoved} {
		emps := groups[group]
		if len(emps) == 0 {
			continue
		}
		title, body := messageFor(eventType, group, res.After)
		keyType := eventType + keySeparator + group
		got, err := send(ctx, pool, approvalchain.EmployeesEvent{
			EventType: eventType, Resource: ResourceInstallAppointment, RecordUUID: res.After.ID,
			ActorUserID: actorUserID, Title: title, Body: body,
			StatusLink:     fmt.Sprintf(statusLinkFormat, res.After.ID),
			EmployeeIDs:    emps,
			IdempotencyKey: IdempotencyKey(res.After.ID, res.After.Version, keyType, emps),
		})
		if err != nil {
			slog.ErrorContext(ctx, "install appointment notify failed", "appointment", res.After.ID, "event", eventType, "group", group, "error", err)
			continue
		}
		ids = append(ids, got...)
	}
	if len(ids) > 0 && len(res.HistoryIDs) > 0 && pool != nil {
		if err := SetHistoryNotifyIDs(ctx, pool, res.HistoryIDs[0], ids); err != nil {
			slog.ErrorContext(ctx, "install appointment notify ids not saved", "appointment", res.After.ID, "error", err)
		}
	}
	sort.Strings(ids)
	return len(ids) > 0, ids
}

// primaryEventType picks the notification event type for a change's actions (first action wins).
func primaryEventType(actions []string) string {
	if len(actions) == 0 {
		return EventUpdated
	}
	return eventTypeFor(actions[0])
}

// capitalize upper-cases the first byte of an ASCII word.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
