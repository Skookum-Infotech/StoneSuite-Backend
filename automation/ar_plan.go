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
	rule, found, enabled, err := loadRuleAnyState(ctx, q, key)
	if found && !enabled {
		return ruleRow{}, false, nil // a disabled rule's config is never decoded for the job
	}
	if err != nil || !found {
		return ruleRow{}, false, err
	}
	return rule, true, nil
}

// loadRuleAnyState reads the rule and its state regardless of the enabled flag.
// found is false when the rule row is missing.
func loadRuleAnyState(ctx context.Context, q Querier, key string) (rule ruleRow, found, enabled bool, err error) {
	var raw []byte
	var en bool
	var enabledAt *time.Time
	scanErr := q.QueryRow(ctx, `
SELECT r.id::text, r.rule, r.enabled, s.enabled_at
FROM automation_rules r
LEFT JOIN automation_state s ON s.rule_id = r.id
WHERE r.rule_key = $1`, key).Scan(&rule.ID, &raw, &en, &enabledAt)
	if errors.Is(scanErr, pgx.ErrNoRows) {
		return ruleRow{}, false, false, nil
	}
	if scanErr != nil {
		return ruleRow{}, false, false, fmt.Errorf("load rule %s: %w", key, scanErr)
	}
	if err := json.Unmarshal(raw, &rule.Config); err != nil {
		return ruleRow{}, true, en, fmt.Errorf("decode rule %s: %w", key, err)
	}
	if enabledAt != nil {
		rule.EnabledOn = *enabledAt
	}
	return rule, true, en, nil
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

// PlanRow is one delivery row the AR planner would write (or did write). Digest
// is empty for rows that carry no digest key.
type PlanRow struct {
	InvoiceUUID string    `json:"invoiceUuid"`
	Number      string    `json:"number"`
	Email       string    `json:"email"`
	Stage       int       `json:"stage"`
	Anchor      time.Time `json:"anchor"`
	RunAt       time.Time `json:"runAt"`
	Status      string    `json:"status"`
	Reason      string    `json:"reason"`
	Digest      string    `json:"-"`
}

// buildARPlanRows lists the candidates and returns every delivery row the planner
// would write for env. It reads only.
func buildARPlanRows(ctx context.Context, q Querier, env JobEnv, rule ruleRow) ([]PlanRow, error) {
	cands, err := listARCandidates(ctx, q, env.Today)
	if err != nil {
		return nil, err
	}
	return planRowsFor(cands, env, rule), nil
}

// planRowsFor is the candidate/stage loop: a pure function of its inputs.
func planRowsFor(cands []ARInvoice, env JobEnv, rule ruleRow) []PlanRow {
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

	var rows []PlanRow
	for _, c := range cands {
		for _, sp := range PlanStages(c.Anchor, offsets, env.Today, enabledOn) {
			row := PlanRow{InvoiceUUID: c.UUID, Number: c.Number, Email: c.Email, Stage: sp.Stage,
				Anchor: c.Anchor, RunAt: env.Now, Status: statusSkipped, Reason: string(sp.Action)}
			if sp.Action == StageActionPlan {
				if c.Email == "" {
					row.Reason = reasonNoEmail
				} else {
					row.Status, row.Reason, row.RunAt = statusPlanned, "", runAt
					row.Digest = DigestKey(rule.ID, c.Email, sendDay)
				}
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// planTx is plan's body, run against an open transaction.
func (j *ARDunning) planTx(ctx context.Context, tx pgx.Tx, env JobEnv, rule ruleRow) (planned, skipped int, err error) {
	cands, err := listARCandidates(ctx, tx, env.Today)
	if err != nil {
		return 0, 0, err
	}
	rows := planRowsFor(cands, env, rule)

	batch := &pgx.Batch{}
	var isPlanned []bool
	for _, row := range rows {
		key := OccurrenceKey(rule.ID, resourceInvoice, row.InvoiceUUID, row.Anchor, row.Stage)
		var digest, reasonArg *string
		if row.Digest != "" {
			digest = &row.Digest
		}
		if row.Reason != "" {
			reasonArg = &row.Reason
		}
		batch.Queue(insertDeliverySQL, rule.ID, resourceInvoice, row.InvoiceUUID, row.Anchor, row.Stage, key,
			recipientExternal, row.Email, digest, row.RunAt, row.Status, reasonArg)
		isPlanned = append(isPlanned, row.Status == statusPlanned)
	}
	uuids := make([]string, 0, len(cands))
	anchors := make([]time.Time, 0, len(cands))
	for _, c := range cands {
		uuids = append(uuids, c.UUID)
		anchors = append(anchors, c.Anchor)
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
