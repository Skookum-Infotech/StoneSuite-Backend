package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	resourceInvoice   = "invoice"
	recipientExternal = "external"
	statusPlanned     = "planned"
	statusSkipped     = "skipped"
)

// ruleRow is one automation_rules row plus its enable date. EnabledOn is the raw
// automation_state.enabled_at (zero when NULL); plan localises it.
type ruleRow struct {
	ID        string
	Config    ARRuleConfig
	EnabledOn time.Time
}

// loadRule reads the rule and its state. enabled is false (and err nil) when the
// rule row is missing or disabled.
func loadRule(ctx context.Context, q Querier, key string) (rule ruleRow, enabled bool, err error) {
	var raw []byte
	var en bool
	var enabledAt *time.Time
	scanErr := q.QueryRow(ctx, `
SELECT r.id::text, r.rule, r.enabled, s.enabled_at
FROM automation_rules r
LEFT JOIN automation_state s ON s.rule_id = r.id
WHERE r.rule_key = $1`, key).Scan(&rule.ID, &raw, &en, &enabledAt)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return ruleRow{}, false, nil
	}
	if scanErr != nil {
		return ruleRow{}, false, fmt.Errorf("load rule %s: %w", key, scanErr)
	}
	if !en {
		return ruleRow{}, false, nil
	}
	if err := json.Unmarshal(raw, &rule.Config); err != nil {
		return ruleRow{}, false, fmt.Errorf("decode rule %s: %w", key, err)
	}
	if enabledAt != nil {
		rule.EnabledOn = *enabledAt
	}
	return rule, true, nil
}

// localDate returns t's calendar date in loc as a midnight value in loc.
func localDate(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

// plan writes the delivery rows for every stage already due, in one
// transaction, and returns how many planned/skipped rows were newly inserted.
func (j *ARDunning) plan(ctx context.Context, pool *pgxpool.Pool, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	ctx = WithSystemActor(ctx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("ar plan begin: %w", err)
	}
	planned, skipped, err = j.planTx(ctx, tx, env, rule)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = fmt.Errorf("%w (rollback: %v)", err, rbErr)
		}
		return 0, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("ar plan commit: %w", err)
	}
	return planned, skipped, nil
}

const insertDeliverySQL = `
INSERT INTO automation_deliveries
    (rule_id, resource, record_id, anchor_date, stage, occurrence_key, recipient_kind,
     recipient_email, digest_key, run_at, status, reason)
VALUES ($1::uuid, $2, $3, $4::date, $5, $6, $7, $8, $9, $10, $11, $12)
ON CONFLICT (occurrence_key) DO NOTHING`

// planTx is plan's body, run against an open transaction.
func (j *ARDunning) planTx(ctx context.Context, tx pgx.Tx, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	cands, err := listARCandidates(ctx, tx, env.Today)
	if err != nil {
		return 0, 0, err
	}
	offsets := make([]int, len(rule.Config.Cadence))
	for i, st := range rule.Config.Cadence {
		offsets[i] = st.OffsetDays
	}
	enabledOn := env.Today // enabled_at NULL: nothing retroactive
	if !rule.EnabledOn.IsZero() {
		enabledOn = localDate(rule.EnabledOn, env.Loc)
	}
	runAt := NextSendWindow(env.Now, env.Loc)

	batch := &pgx.Batch{}
	var isPlanned []bool
	var uuids []string
	var anchors []time.Time
	for _, c := range cands {
		uuids = append(uuids, c.UUID)
		anchors = append(anchors, c.Anchor)
		for _, sp := range PlanStages(c.Anchor, offsets, env.Today, enabledOn) {
			key := OccurrenceKey(rule.ID, resourceInvoice, c.UUID, c.Anchor, sp.Stage)
			status, reason, rowRunAt := statusSkipped, string(sp.Action), env.Now
			var digest *string
			if sp.Action == StageActionPlan {
				if c.Email == "" {
					reason = reasonNoEmail
				} else {
					status, reason, rowRunAt = statusPlanned, "", runAt
					d := DigestKey(rule.ID, c.Email, env.Today)
					digest = &d
				}
			}
			var reasonArg *string
			if reason != "" {
				reasonArg = &reason
			}
			batch.Queue(insertDeliverySQL, rule.ID, resourceInvoice, c.UUID, c.Anchor, sp.Stage, key,
				recipientExternal, c.Email, digest, rowRunAt, status, reasonArg)
			isPlanned = append(isPlanned, status == statusPlanned)
		}
	}

	if batch.Len() > 0 {
		br := tx.SendBatch(ctx, batch)
		for _, p := range isPlanned {
			tag, execErr := br.Exec()
			if execErr != nil {
				_ = br.Close()
				return 0, 0, fmt.Errorf("ar plan insert: %w", execErr)
			}
			if tag.RowsAffected() == 0 {
				continue
			}
			if p {
				planned++
			} else {
				skipped++
			}
		}
		if err := br.Close(); err != nil {
			return 0, 0, fmt.Errorf("ar plan batch close: %w", err)
		}
	}

	if len(uuids) > 0 {
		if _, err := tx.Exec(ctx, `
UPDATE automation_deliveries d
SET status = 'skipped', reason = $4, updated_at = now()
FROM unnest($2::text[], $3::date[]) AS cur(record_id, anchor)
WHERE d.rule_id = $1::uuid AND d.resource = $5 AND d.record_id = cur.record_id
  AND d.status IN ('planned', 'failed') AND d.anchor_date <> cur.anchor`,
			rule.ID, uuids, anchors, reasonAnchorChanged, resourceInvoice); err != nil {
			return 0, 0, fmt.Errorf("ar plan supersede anchors: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE automation_state SET last_planned_at = $2 WHERE rule_id = $1::uuid`, rule.ID, env.Now); err != nil {
		return 0, 0, fmt.Errorf("ar plan state: %w", err)
	}
	return planned, skipped, nil
}
