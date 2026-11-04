package installschedule

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// History actions recorded in install_appointment_history.
const (
	ActionCreate     = "create"
	ActionReschedule = "reschedule"
	ActionReassign   = "reassign"
	ActionUpdate     = "update"
	ActionCancel     = "cancel"
	ActionComplete   = "complete"
)

// HistorySnapshot is the post-change state stored with each history row.
type HistorySnapshot struct {
	Appointment Appointment `json:"appointment"`
	CrewIDs     []int       `json:"crewIds"`
}

// HistoryRow is one audit entry to insert.
type HistoryRow struct {
	AppointmentID         int
	Action                string
	FromStart, FromEnd    *time.Time
	ToStart, ToEnd        *time.Time
	Reason                string
	ConflictsAcknowledged []Conflict
	Snapshot              HistorySnapshot
	ActorEmployeeID       int
}

// writeHistory inserts a history row and returns its id.
func writeHistory(ctx context.Context, q Querier, h HistoryRow) (int64, error) {
	snap, err := json.Marshal(h.Snapshot)
	if err != nil {
		return 0, fmt.Errorf("marshal history snapshot: %w", err)
	}
	var acked *string
	if len(h.ConflictsAcknowledged) > 0 {
		b, err := json.Marshal(h.ConflictsAcknowledged)
		if err != nil {
			return 0, fmt.Errorf("marshal acknowledged conflicts: %w", err)
		}
		s := string(b)
		acked = &s
	}
	var reason *string
	if h.Reason != "" {
		reason = &h.Reason
	}
	var id int64
	err = q.QueryRow(ctx, `
		INSERT INTO install_appointment_history
			(install_appointment_id, action, from_starts_at, from_ends_at, to_starts_at, to_ends_at,
			 reason, conflicts_acknowledged, snapshot, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10)
		RETURNING install_appointment_history_id`,
		h.AppointmentID, h.Action, h.FromStart, h.FromEnd, h.ToStart, h.ToEnd,
		reason, acked, string(snap), h.ActorEmployeeID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert appointment history: %w", err)
	}
	return id, nil
}

// SetHistoryNotifyIDs records the notification ids sent for a history row.
func SetHistoryNotifyIDs(ctx context.Context, q Querier, historyID int64, ids []string) error {
	if _, err := q.Exec(ctx,
		`UPDATE install_appointment_history SET notify_notification_ids = $2 WHERE install_appointment_history_id = $1`,
		historyID, ids); err != nil {
		return fmt.Errorf("set history notify ids: %w", err)
	}
	return nil
}
