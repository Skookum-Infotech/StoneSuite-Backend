package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// installCustomerPart is the recipient segment of the occurrence key; a visit has one customer.
const installCustomerPart = "customer"

// planInstall writes the delivery rows for every install stage already due, in one transaction, and
// returns how many planned/skipped rows were newly inserted.
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

// planInstallTx is planInstall's body, run against an open transaction. The rows are customer-facing
// (recipient kind external), so the drain applies the tenant kill switch, the suppression list and
// the per-contact and daily caps on top of the live re-check.
func planInstallTx(ctx context.Context, tx pgx.Tx, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	rows, err := listInstallCandidates(ctx, tx, env.Today)
	if err != nil {
		return 0, 0, err
	}
	uuids := make([]string, 0, len(rows))
	anchors := make([]time.Time, 0, len(rows))
	for _, r := range rows {
		uuids = append(uuids, r.UUID)
		anchors = append(anchors, r.LocalStart)
	}
	if planned, skipped, err = insertDeliveries(ctx, tx, rule.ID, resourceInstallAppointment, recipientExternal, installInsertsFor(rows, env, rule)); err != nil {
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

// installInsertsFor is the visit/stage loop: a pure function of its inputs. A customer with several
// installations the same day gets one email (the digest key is per address and day).
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
	sendDay := localDate(runAt, env.Loc)

	var out []deliveryInsert
	for _, r := range rows {
		for _, sp := range PlanStages(r.LocalStart, offsets, env.Today, enabledOn) {
			ins := deliveryInsert{RecordID: r.UUID, Anchor: r.LocalStart, Stage: sp.Stage,
				Key:   OccurrenceKey(rule.ID, resourceInstallAppointment, r.UUID+"~"+installCustomerPart, r.LocalStart, sp.Stage),
				Email: r.ContactEmail, RunAt: env.Now, Status: statusSkipped}
			reason := string(sp.Action)
			if sp.Action == StageActionPlan {
				switch {
				case r.CustomerDeleted:
					reason = reasonCustomerGone
				case r.DoNotContact:
					reason = reasonDoNotContact
				case r.ContactEmail == "":
					reason = reasonNoEmail
				default:
					digest := DigestKey(rule.ID, r.ContactEmail, sendDay)
					ins.Status, ins.RunAt, ins.Digest, reason = statusPlanned, runAt, &digest, ""
				}
			}
			if reason != "" {
				ins.Reason = &reason
			}
			out = append(out, ins)
		}
	}
	return out
}
