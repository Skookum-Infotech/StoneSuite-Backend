package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// deliveryInsert is one automation_deliveries row a planner wants written.
// Digest, UserID and Reason are NULL when nil.
type deliveryInsert struct {
	RecordID string
	Anchor   time.Time
	Stage    int
	Key      string
	Email    string
	UserID   *string
	Digest   *string
	RunAt    time.Time
	Status   string
	Reason   *string
}

// insertDeliveries batch-inserts rows (ON CONFLICT DO NOTHING) and returns how
// many planned and skipped rows were newly written.
func insertDeliveries(ctx context.Context, tx pgx.Tx, ruleID, resource, kind string, rows []deliveryInsert) (planned, skipped int, err error) {
	if len(rows) == 0 {
		return 0, 0, nil
	}
	batch := &pgx.Batch{}
	for _, r := range rows {
		batch.Queue(insertDeliverySQL, ruleID, resource, r.RecordID, r.Anchor, r.Stage, r.Key,
			kind, r.Email, r.Digest, r.RunAt, r.Status, r.Reason, r.UserID)
	}
	br := tx.SendBatch(ctx, batch)
	for _, r := range rows {
		tag, execErr := br.Exec()
		if execErr != nil {
			_ = br.Close()
			return 0, 0, fmt.Errorf("%s plan insert: %w", resource, execErr)
		}
		if tag.RowsAffected() == 0 {
			continue
		}
		if r.Status == statusPlanned {
			planned++
		} else {
			skipped++
		}
	}
	if err := br.Close(); err != nil {
		return 0, 0, fmt.Errorf("%s plan batch close: %w", resource, err)
	}
	return planned, skipped, nil
}

// supersedeAnchors skips open rows whose record's anchor date has since changed.
func supersedeAnchors(ctx context.Context, tx pgx.Tx, ruleID, resource string, uuids []string, anchors []time.Time) error {
	if len(uuids) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
UPDATE automation_deliveries d
SET status = 'skipped', reason = $4, updated_at = now()
FROM unnest($2::text[], $3::date[]) AS cur(record_id, anchor)
WHERE d.rule_id = $1::uuid AND d.resource = $5 AND d.record_id = cur.record_id
  AND d.status IN ('planned', 'failed') AND d.anchor_date <> cur.anchor`,
		ruleID, uuids, anchors, reasonAnchorChanged, resource); err != nil {
		return fmt.Errorf("%s plan supersede anchors: %w", resource, err)
	}
	return nil
}

// markPlanned stamps the rule's last_planned_at.
func markPlanned(ctx context.Context, tx pgx.Tx, ruleID string, now time.Time) error {
	if _, err := tx.Exec(ctx, `UPDATE automation_state SET last_planned_at = $2 WHERE rule_id = $1::uuid`, ruleID, now); err != nil {
		return fmt.Errorf("plan state: %w", err)
	}
	return nil
}
