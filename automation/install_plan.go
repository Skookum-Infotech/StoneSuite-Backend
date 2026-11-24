package automation

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// planInstall writes the delivery rows for every install stage already due, in
// one transaction, and returns how many planned/skipped rows were newly inserted.
func planInstall(ctx context.Context, pool *pgxpool.Pool, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	ctx = WithSystemActor(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("install plan begin: %w", err)
	}
	planned, skipped, err = planInstallTx(ctx, tx, env, rule)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("install plan commit: %w", err)
	}
	return planned, skipped, nil
}

// planInstallTx is planInstall's body, run against an open transaction.
func planInstallTx(ctx context.Context, tx pgx.Tx, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	rows, err := listInstallCandidates(ctx, tx, env.Today)
	if err != nil {
		return 0, 0, err
	}
	seen := map[string]bool{}
	var uuids []string
	var anchors []time.Time
	for _, r := range rows {
		if !seen[r.UUID] {
			seen[r.UUID] = true
			uuids = append(uuids, r.UUID)
			anchors = append(anchors, r.LocalStart)
		}
	}
	if planned, skipped, err = insertDeliveries(ctx, tx, rule.ID, resourceInstallAppointment, recipientInternal, installInsertsFor(rows, env, rule)); err != nil {
		return 0, 0, err
	}
	if err := supersedeAnchors(ctx, tx, rule.ID, resourceInstallAppointment, uuids, anchors); err != nil {
		return 0, 0, err
	}
	if err := markPlanned(ctx, tx, rule.ID, env.Now); err != nil {
		return 0, 0, err
	}
	return planned, skipped, nil
}

// installInsertsFor is the visit/crew/stage loop: a pure function of its inputs.
// Each crew member has their own occurrence key, so a crew change adds rows
// for the newcomer without re-sending to everyone else.
func installInsertsFor(rows []InstallRow, env JobEnv, rule ruleRow) []deliveryInsert {
	offsets := make([]int, len(rule.Config.Cadence))
	for i, st := range rule.Config.Cadence {
		offsets[i] = st.OffsetDays
	}
	enabledOn := env.Today // enabled_at NULL: nothing retroactive
	if !rule.EnabledOn.IsZero() {
		enabledOn = localDate(rule.EnabledOn, env.Loc)
	}
	runAt := NextSendWindow(env.Now, env.Loc)
	sendDay := localDate(runAt, env.Loc) // one recipient gets one digest per day across visits

	var out []deliveryInsert
	for _, r := range rows {
		part := r.UserID
		if part == "" {
			part = "e" + strconv.Itoa(r.EmployeeID) // crew member without an active user
		}
		for _, sp := range PlanStages(r.LocalStart, offsets, env.Today, enabledOn) {
			ins := deliveryInsert{RecordID: r.UUID, Anchor: r.LocalStart, Stage: sp.Stage,
				Key:   OccurrenceKey(rule.ID, resourceInstallAppointment, r.UUID+"~"+part, r.LocalStart, sp.Stage),
				Email: r.Email, RunAt: env.Now, Status: statusSkipped}
			reason := string(sp.Action)
			if sp.Action == StageActionPlan {
				if r.Email == "" {
					reason = reasonNoEmail
				} else {
					digest := DigestKey(rule.ID, r.Email, sendDay)
					ins.Status, ins.RunAt, ins.Digest, reason = statusPlanned, runAt, &digest, ""
				}
			}
			if reason != "" {
				ins.Reason = &reason
			}
			if r.UserID != "" {
				uid := r.UserID
				ins.UserID = &uid
			}
			out = append(out, ins)
		}
	}
	return out
}
