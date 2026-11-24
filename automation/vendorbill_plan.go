package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// planVendorBills writes the delivery rows for every vendor bill stage already
// due, in one transaction, and returns how many planned/skipped rows were newly inserted.
func planVendorBills(ctx context.Context, pool *pgxpool.Pool, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	ctx = WithSystemActor(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("vendor bill plan begin: %w", err)
	}
	planned, skipped, err = planVendorBillsTx(ctx, tx, env, rule)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("vendor bill plan commit: %w", err)
	}
	return planned, skipped, nil
}

// planVendorBillsTx is the body of planVendorBills, run against an open transaction.
func planVendorBillsTx(ctx context.Context, tx pgx.Tx, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	cands, err := listVendorBillCandidates(ctx, tx, env.Today)
	if err != nil {
		return 0, 0, err
	}
	uuids := make([]string, 0, len(cands))
	anchors := make([]time.Time, 0, len(cands))
	targets := make([]vendorBillTarget, 0, len(cands))
	for _, c := range cands {
		rcpts, err := vendorBillRecipients(ctx, tx, c)
		if err != nil {
			return 0, 0, err
		}
		targets = append(targets, vendorBillTarget{Doc: c, Rcpts: rcpts})
		uuids = append(uuids, c.UUID)
		anchors = append(anchors, c.DueDate)
	}
	if planned, skipped, err = insertDeliveries(ctx, tx, rule.ID, resourceVendorBill, recipientInternal, vendorBillInsertsFor(targets, env, rule)); err != nil {
		return 0, 0, err
	}
	if err := supersedeAnchors(ctx, tx, rule.ID, resourceVendorBill, uuids, anchors); err != nil {
		return 0, 0, err
	}
	if err := markPlanned(ctx, tx, rule.ID, env.Now); err != nil {
		return 0, 0, err
	}
	return planned, skipped, nil
}

// vendorBillTarget is a bill with the people responsible for it.
type vendorBillTarget struct {
	Doc   VendorBillDoc
	Rcpts []vendorBillRecipient
}

// vendorBillNoRecipientPart is the recipient segment of the occurrence key for a bill nobody is
// responsible for (one skipped no_email row per stage).
const vendorBillNoRecipientPart = "none"

// vendorBillInsertsFor is the bill/recipient/stage loop: a pure function of its inputs. Each recipient has
// their own occurrence key, and one person gets one digest per day across all their bills.
func vendorBillInsertsFor(targets []vendorBillTarget, env JobEnv, rule ruleRow) []deliveryInsert {
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
	for _, t := range targets {
		c := t.Doc
		stages := PlanStages(c.DueDate, offsets, env.Today, enabledOn)
		if len(t.Rcpts) == 0 {
			for _, sp := range stages {
				reason := string(sp.Action)
				if sp.Action == StageActionPlan {
					reason = reasonNoEmail
				}
				out = append(out, vendorBillRow(rule, c, vendorBillNoRecipientPart, sp.Stage, "", "", reason, env))
			}
			continue
		}
		for _, rc := range t.Rcpts {
			for _, sp := range stages {
				ins := vendorBillRow(rule, c, rc.UserID, sp.Stage, rc.Email, rc.UserID, string(sp.Action), env)
				if sp.Action == StageActionPlan {
					digest := DigestKey(rule.ID, rc.Email, sendDay)
					ins.Status, ins.RunAt, ins.Digest, ins.Reason = statusPlanned, runAt, &digest, nil
				}
				out = append(out, ins)
			}
		}
	}
	return out
}

// vendorBillRow builds a skipped row for one (bill, recipient part, stage); the caller upgrades it to
// planned when a send is due.
func vendorBillRow(rule ruleRow, c VendorBillDoc, part string, stage int, email, userID, reason string, env JobEnv) deliveryInsert {
	ins := deliveryInsert{RecordID: c.UUID, Anchor: c.DueDate, Stage: stage, Email: email, RunAt: env.Now, Status: statusSkipped,
		Key: OccurrenceKey(rule.ID, resourceVendorBill, c.UUID+"~"+part, c.DueDate, stage)}
	if reason != "" {
		ins.Reason = &reason
	}
	if userID != "" {
		uid := userID
		ins.UserID = &uid
	}
	return ins
}
