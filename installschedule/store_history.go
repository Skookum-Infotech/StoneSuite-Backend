package installschedule

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// historyReadLimit caps the history rows returned for one appointment.
const historyReadLimit = 200

// HistoryEntry is one audit row as shown to a caller who may read the appointment.
// Acknowledged conflicts are reduced to a count: the stored list names other appointments
// the caller may not be allowed to see.
type HistoryEntry struct {
	ID                         int64      `json:"id"`
	Action                     string     `json:"action"`
	At                         time.Time  `json:"at"`
	FromStartsAt               *time.Time `json:"fromStartsAt"`
	FromEndsAt                 *time.Time `json:"fromEndsAt"`
	ToStartsAt                 *time.Time `json:"toStartsAt"`
	ToEndsAt                   *time.Time `json:"toEndsAt"`
	Reason                     string     `json:"reason"`
	ConflictsAcknowledgedCount int        `json:"conflictsAcknowledgedCount"`
	ActorName                  string     `json:"actorName"`
	ActorEmployeeID            int        `json:"actorEmployeeId"`
}

// History lists an appointment's audit rows, newest first, or ErrNotFound for an unknown uuid.
func History(ctx context.Context, q Querier, uuid string) ([]HistoryEntry, error) {
	var apptID int
	err := q.QueryRow(ctx, `SELECT install_appointment_id FROM install_appointment WHERE install_appointment_uuid = $1`, uuid).Scan(&apptID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find appointment for history: %w", err)
	}
	rows, err := q.Query(ctx, `
		SELECT h.install_appointment_history_id, h.action, h.at,
		       h.from_starts_at, h.from_ends_at, h.to_starts_at, h.to_ends_at,
		       COALESCE(h.reason,''),
		       COALESCE(jsonb_array_length(h.conflicts_acknowledged), 0),
		       btrim(e.employee_first_name || ' ' || e.employee_last_name), h.actor_employee_id
		FROM install_appointment_history h
		JOIN employee e ON e.employee_id = h.actor_employee_id
		WHERE h.install_appointment_id = $1
		ORDER BY h.at DESC, h.install_appointment_history_id DESC
		LIMIT $2`, apptID, historyReadLimit)
	if err != nil {
		return nil, fmt.Errorf("list appointment history: %w", err)
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.ID, &h.Action, &h.At, &h.FromStartsAt, &h.FromEndsAt, &h.ToStartsAt, &h.ToEndsAt,
			&h.Reason, &h.ConflictsAcknowledgedCount, &h.ActorName, &h.ActorEmployeeID); err != nil {
			return nil, fmt.Errorf("scan appointment history: %w", err)
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate appointment history: %w", err)
	}
	return out, nil
}
