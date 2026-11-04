package installschedule

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/domainevent"
)

// ResourceInstallAppointment is the resource name used by domain events and notifications.
const ResourceInstallAppointment = "install_appointment"

// Domain event types emitted by this package.
const (
	EventScheduled   = "install_appointment.scheduled"
	EventRescheduled = "install_appointment.rescheduled"
	EventReassigned  = "install_appointment.reassigned"
	EventUpdated     = "install_appointment.updated"
	EventCancelled   = "install_appointment.cancelled"
	EventCompleted   = "install_appointment.completed"
)

// Changed-field keys carried in event payloads.
const (
	changedStartsAt  = "startsAt"
	changedEndsAt    = "endsAt"
	changedAllDay    = "allDay"
	changedCrew      = "crew"
	changedVisitType = "visitType"
	changedNotes     = "notes"
	changedStatus    = "status"
)

// eventPayload is the domain event payload: ids and changed keys only, never notes or names.
type eventPayload struct {
	FabricationJobID string   `json:"fabricationJobId"`
	VisitType        string   `json:"visitType"`
	Changed          []string `json:"changed"`
	Version          int      `json:"version"`
}

// eventTypeFor maps a history action to its domain event type.
func eventTypeFor(action string) string {
	switch action {
	case ActionCreate:
		return EventScheduled
	case ActionReschedule:
		return EventRescheduled
	case ActionReassign:
		return EventReassigned
	case ActionCancel:
		return EventCancelled
	case ActionComplete:
		return EventCompleted
	default:
		return EventUpdated
	}
}

// emitEvent inserts one domain event inside tx, so it commits or rolls back with the appointment change.
func emitEvent(ctx context.Context, tx pgx.Tx, eventType string, a *Appointment, changed []string, actorUserID string) error {
	if changed == nil {
		changed = []string{}
	}
	err := domainevent.Emit(ctx, tx, domainevent.Event{
		Type: eventType, Resource: ResourceInstallAppointment, RecordID: a.ID, ActorUserID: actorUserID,
		Payload: eventPayload{FabricationJobID: a.FabricationJobID, VisitType: string(a.VisitType), Changed: changed, Version: a.Version},
	})
	if err != nil {
		return fmt.Errorf("emit %s: %w", eventType, err)
	}
	return nil
}

// emitChangeEvents emits one event per action recorded in res.
func emitChangeEvents(ctx context.Context, tx pgx.Tx, res *ChangeResult, actorUserID string) error {
	for _, action := range res.Actions {
		if err := emitEvent(ctx, tx, eventTypeFor(action), res.After, changedKeys(action, res.Before, res.After), actorUserID); err != nil {
			return err
		}
	}
	return nil
}

// changedKeys lists the fields an action changed, by comparing the before and after states.
func changedKeys(action string, before, after *Appointment) []string {
	switch action {
	case ActionReschedule:
		var keys []string
		if !before.StartsAt.Equal(after.StartsAt) {
			keys = append(keys, changedStartsAt)
		}
		if !before.EndsAt.Equal(after.EndsAt) {
			keys = append(keys, changedEndsAt)
		}
		if before.AllDay != after.AllDay {
			keys = append(keys, changedAllDay)
		}
		return keys
	case ActionReassign:
		return []string{changedCrew}
	case ActionUpdate:
		var keys []string
		if before.VisitType != after.VisitType {
			keys = append(keys, changedVisitType)
		}
		if before.Notes != after.Notes {
			keys = append(keys, changedNotes)
		}
		return keys
	case ActionCancel, ActionComplete:
		return []string{changedStatus}
	default:
		return nil
	}
}
