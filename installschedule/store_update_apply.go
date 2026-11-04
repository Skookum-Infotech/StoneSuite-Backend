package installschedule

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// applyUpdate writes the appointment row (bumping record_version by one) and reconciles the crew.
func applyUpdate(ctx context.Context, tx pgx.Tx, ref apptRef, p updatePlan, expectedVersion, actor int) error {
	tag, err := tx.Exec(ctx, `
		UPDATE install_appointment
		SET visit_type = $3, starts_at = $4, ends_at = $5, all_day = $6, local_start_date = $7, local_end_date = $8,
		    timezone = $9, notes = NULLIF($10,''), updated_at = now(), updated_by = $11,
		    record_version = record_version + 1
		WHERE install_appointment_id = $1 AND record_version = $2 AND appointment_status = 'scheduled'`,
		ref.ID, expectedVersion, string(p.visitType), p.iv.Start, p.iv.End, p.iv.AllDay,
		p.iv.LocalStartDate, p.iv.LocalEndDate, p.iv.TZ, p.notes, actor)
	if err != nil {
		return fmt.Errorf("update appointment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return staleAfterMiss(ctx, tx, ref.ID)
	}
	if !p.crewChanged {
		return nil
	}
	return replaceCrew(ctx, tx, ref.ID, p.crew, actor)
}

// replaceCrew makes the stored crew equal to crew, keeping assignment stamps of retained members.
func replaceCrew(ctx context.Context, tx pgx.Tx, apptID int, crew []CrewInput, actor int) error {
	ids := crewIDs(crew)
	if _, err := tx.Exec(ctx,
		`DELETE FROM install_appointment_crew WHERE install_appointment_id = $1 AND employee_id <> ALL($2)`,
		apptID, ids); err != nil {
		return fmt.Errorf("remove appointment crew: %w", err)
	}
	// Demote first so promoting a new lead never violates the one-lead unique index mid-way.
	if _, err := tx.Exec(ctx,
		`UPDATE install_appointment_crew SET crew_role = $3 WHERE install_appointment_id = $1 AND crew_role = $2`,
		apptID, string(CrewLead), string(CrewMemberRole)); err != nil {
		return fmt.Errorf("demote appointment crew: %w", err)
	}
	for _, c := range crew {
		if _, err := tx.Exec(ctx, `
			INSERT INTO install_appointment_crew (install_appointment_id, employee_id, crew_role, assigned_by)
			VALUES ($1,$2,$3,$4)
			ON CONFLICT (install_appointment_id, employee_id) DO UPDATE SET crew_role = EXCLUDED.crew_role`,
			apptID, c.EmployeeID, string(c.Role), actor); err != nil {
			return fmt.Errorf("assign appointment crew %d: %w", c.EmployeeID, err)
		}
	}
	return nil
}

// recordUpdateHistory writes one history row per action taken, in the order reschedule, reassign, update.
// The acknowledged conflicts are recorded once, on the first row.
func recordUpdateHistory(ctx context.Context, tx pgx.Tx, res *ChangeResult, apptID int, p updatePlan, reason string, acked []Conflict, actor int) error {
	var actions []string
	if p.timeChanged {
		actions = append(actions, ActionReschedule)
	}
	if p.crewChanged {
		actions = append(actions, ActionReassign)
	}
	if p.fieldsChanged {
		actions = append(actions, ActionUpdate)
	}
	snap := snapshotOf(res.After)
	for i, action := range actions {
		row := HistoryRow{
			AppointmentID: apptID, Action: action, Reason: reason, Snapshot: snap, ActorEmployeeID: actor,
			FromStart: &res.Before.StartsAt, FromEnd: &res.Before.EndsAt, ToStart: &res.After.StartsAt, ToEnd: &res.After.EndsAt,
		}
		if i == 0 {
			row.ConflictsAcknowledged = acked
		}
		id, err := writeHistory(ctx, tx, row)
		if err != nil {
			return err
		}
		res.HistoryIDs = append(res.HistoryIDs, id)
	}
	res.Actions = actions
	return nil
}
