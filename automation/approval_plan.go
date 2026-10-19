package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// approvalNoRecipientPart is the recipient segment of the occurrence key for a
// record that has nobody to remind (one skipped no_email row).
const approvalNoRecipientPart = "none"

// approvalTarget is a stuck record with the recipients resolved for it.
type approvalTarget struct {
	Rec   ApprovalRecord
	Rcpts []approvalRecipient
}

// planApprovals writes the delivery rows for every stuck-approval stage already
// due, across all gates, in one transaction, and returns how many planned/skipped
// rows were newly inserted.
func planApprovals(ctx context.Context, pool *pgxpool.Pool, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	ctx = WithSystemActor(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("approval plan begin: %w", err)
	}
	planned, skipped, err = planApprovalsTx(ctx, tx, env, rule)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("approval plan commit: %w", err)
	}
	return planned, skipped, nil
}

// planApprovalsTx is planApprovals' body, run against an open transaction.
func planApprovalsTx(ctx context.Context, tx pgx.Tx, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	uuidsBy := map[string][]string{}
	anchorsBy := map[string][]time.Time{}
	for _, spec := range approvalSpecs() {
		recs, err := listApprovalCandidates(ctx, tx, spec, env.Today, env.Loc)
		if err != nil {
			return 0, 0, err
		}
		targets := make([]approvalTarget, 0, len(recs))
		for _, rec := range recs {
			rcpts, err := approvalRecipients(ctx, tx, spec, rec)
			if err != nil {
				return 0, 0, err
			}
			targets = append(targets, approvalTarget{Rec: rec, Rcpts: rcpts})
			uuidsBy[spec.Workflow] = append(uuidsBy[spec.Workflow], rec.UUID)
			anchorsBy[spec.Workflow] = append(anchorsBy[spec.Workflow], rec.Entered)
		}
		p, s, err := insertDeliveries(ctx, tx, rule.ID, spec.Workflow, recipientInternal, approvalInsertsFor(spec, targets, env, rule))
		if err != nil {
			return 0, 0, err
		}
		planned += p
		skipped += s
	}
	for _, spec := range approvalSpecs() {
		wf := spec.Workflow
		uu, aa := uuidsBy[wf], anchorsBy[wf]
		if len(uu) == 0 {
			continue
		}
		delete(uuidsBy, wf)
		if err := supersedeAnchors(ctx, tx, rule.ID, wf, uu, aa); err != nil {
			return 0, 0, err
		}
	}
	if err := markPlanned(ctx, tx, rule.ID, env.Now); err != nil {
		return 0, 0, err
	}
	return planned, skipped, nil
}

// approvalInsertsFor is the target/recipient/stage loop: a pure function of its inputs.
func approvalInsertsFor(spec approvalSpec, targets []approvalTarget, env JobEnv, rule ruleRow) []deliveryInsert {
	offsets := make([]int, len(rule.Config.Cadence))
	for i, st := range rule.Config.Cadence {
		offsets[i] = st.OffsetDays
	}
	enabledOn := env.Today // enabled_at NULL: nothing retroactive
	if !rule.EnabledOn.IsZero() {
		enabledOn = localDate(rule.EnabledOn, env.Loc)
	}
	runAt := NextSendWindow(env.Now, env.Loc)
	sendDay := localDate(runAt, env.Loc) // one approver gets one digest per day across modules

	var out []deliveryInsert
	for _, t := range targets {
		stages := PlanStages(t.Rec.Entered, offsets, env.Today, enabledOn)
		if len(t.Rcpts) == 0 {
			for _, sp := range stages {
				reason := string(sp.Action)
				if sp.Action == StageActionPlan {
					reason = reasonNoEmail
				}
				out = append(out, approvalSkipRow(spec, t.Rec, approvalNoRecipientPart, sp.Stage, rule, env, "", reason))
			}
			continue
		}
		for _, rc := range t.Rcpts {
			for _, sp := range stages {
				ins := approvalSkipRow(spec, t.Rec, rc.UserID, sp.Stage, rule, env, rc.Email, string(sp.Action))
				if sp.Action == StageActionPlan {
					if rc.Email == "" {
						reason := reasonNoEmail
						ins.Reason = &reason
					} else {
						digest := DigestKey(rule.ID, rc.Email, sendDay)
						ins.Status, ins.RunAt, ins.Digest, ins.Reason = statusPlanned, runAt, &digest, nil
					}
				}
				if rc.UserID != "" {
					uid := rc.UserID
					ins.UserID = &uid
				}
				out = append(out, ins)
			}
		}
	}
	return out
}

// approvalSkipRow builds a skipped row for one (record, recipient part, stage); the
// caller upgrades it to planned when a send is due.
func approvalSkipRow(spec approvalSpec, rec ApprovalRecord, recipientPart string, stage int, rule ruleRow, env JobEnv, email, reason string) deliveryInsert {
	key := OccurrenceKey(rule.ID, spec.Workflow, rec.UUID+"~"+spec.Gate.StatusCode+"~"+recipientPart, rec.Entered, stage)
	return deliveryInsert{RecordID: rec.UUID, Anchor: rec.Entered, Stage: stage, Key: key,
		Email: email, RunAt: env.Now, Status: statusSkipped, Reason: &reason}
}
