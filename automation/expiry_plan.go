package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// planExpiry writes the delivery rows for every expiry stage already due, in one
// transaction, and returns how many planned/skipped rows were newly inserted.
func planExpiry(ctx context.Context, pool *pgxpool.Pool, env JobEnv, spec expirySpec, rule ruleRow) (planned, skipped int, err error) {
	ctx = WithSystemActor(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("%s plan begin: %w", spec.Resource, err)
	}
	planned, skipped, err = planExpiryTx(ctx, tx, env, spec, rule)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("%s plan commit: %w", spec.Resource, err)
	}
	return planned, skipped, nil
}

// planExpiryTx is planExpiry's body, run against an open transaction.
func planExpiryTx(ctx context.Context, tx pgx.Tx, env JobEnv, spec expirySpec, rule ruleRow) (planned, skipped int, err error) {
	cands, err := listExpiryCandidates(ctx, tx, spec, env.Today)
	if err != nil {
		return 0, 0, err
	}
	inserts := expiryInsertsFor(cands, spec, env, rule)
	uuids := make([]string, 0, len(cands))
	anchors := make([]time.Time, 0, len(cands))
	for _, c := range cands {
		uuids = append(uuids, c.UUID)
		anchors = append(anchors, c.ValidUntil)
	}
	if planned, skipped, err = insertDeliveries(ctx, tx, rule.ID, spec.Resource, recipientInternal, inserts); err != nil {
		return 0, 0, err
	}
	if err := supersedeAnchors(ctx, tx, rule.ID, spec.Resource, uuids, anchors); err != nil {
		return 0, 0, err
	}
	if err := markPlanned(ctx, tx, rule.ID, env.Now); err != nil {
		return 0, 0, err
	}
	return planned, skipped, nil
}

// expiryInsertsFor is the candidate/stage loop: a pure function of its inputs.
func expiryInsertsFor(cands []ExpiryDoc, spec expirySpec, env JobEnv, rule ruleRow) []deliveryInsert {
	offsets := make([]int, len(rule.Config.Cadence))
	for i, st := range rule.Config.Cadence {
		offsets[i] = st.OffsetDays
	}
	enabledOn := env.Today // enabled_at NULL: nothing retroactive
	if !rule.EnabledOn.IsZero() {
		enabledOn = localDate(rule.EnabledOn, env.Loc)
	}
	runAt := NextSendWindow(env.Now, env.Loc)
	sendDay := localDate(runAt, env.Loc) // rows sent the same morning share one digest

	var out []deliveryInsert
	for _, c := range cands {
		for _, sp := range PlanStages(c.ValidUntil, offsets, env.Today, enabledOn) {
			ins := deliveryInsert{RecordID: c.UUID, Anchor: c.ValidUntil, Stage: sp.Stage,
				Key:   OccurrenceKey(rule.ID, spec.Resource, c.UUID, c.ValidUntil, sp.Stage),
				Email: c.OwnerEmail, RunAt: env.Now, Status: statusSkipped}
			reason := string(sp.Action)
			if sp.Action == StageActionPlan {
				if c.OwnerEmail == "" {
					reason = reasonNoEmail
				} else {
					digest := DigestKey(rule.ID, c.OwnerEmail, sendDay)
					ins.Status, ins.RunAt, ins.Digest, reason = statusPlanned, runAt, &digest, ""
				}
			}
			if reason != "" {
				ins.Reason = &reason
			}
			if c.OwnerUserID != "" {
				uid := c.OwnerUserID
				ins.UserID = &uid
			}
			out = append(out, ins)
		}
	}
	return out
}
