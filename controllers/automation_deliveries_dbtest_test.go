//go:build dbtest

package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutomationDeliveries_KeysetPagination: rows sharing one created_at are
// paged exactly once each across limit=2 pages.
func TestAutomationDeliveries_KeysetPagination(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	ctx := context.Background()
	const ruleKey = "dbtest_pagination_rule"
	var ruleID string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO automation_rules (rule_key, name, audience, is_system) VALUES ($1, 'dbtest', 'internal', FALSE)
		 ON CONFLICT (rule_key) DO UPDATE SET name = EXCLUDED.name RETURNING id::text`, ruleKey).Scan(&ruleID))
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM automation_deliveries WHERE rule_id = $1`, ruleID)
		_, _ = pool.Exec(ctx, `DELETE FROM automation_rules WHERE id = $1`, ruleID)
	}
	cleanup()
	_, err := pool.Exec(ctx, `INSERT INTO automation_rules (id, rule_key, name, audience, is_system)
		VALUES ($1, $2, 'dbtest', 'internal', FALSE)`, ruleID, ruleKey)
	require.NoError(t, err)
	t.Cleanup(cleanup)

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, rec := range []string{"r1", "r2", "r3"} {
		_, err := pool.Exec(ctx, `INSERT INTO automation_deliveries
			(rule_id, resource, record_id, anchor_date, stage, occurrence_key, recipient_kind, run_at, created_at)
			VALUES ($1, 'invoice', $2, '2026-01-01', 0, $3, 'internal', NOW(), $4)`,
			ruleID, rec, ruleKey+"|"+rec, created)
		require.NoError(t, err)
	}

	sentAt := time.Date(2026, 2, 3, 4, 5, 6, 789000000, time.UTC)
	_, err = pool.Exec(ctx, `INSERT INTO automation_deliveries
		(rule_id, resource, record_id, anchor_date, stage, occurrence_key, recipient_kind, run_at, created_at, status, sent_at)
		VALUES ($1, 'invoice', 'r4', '2026-01-01', 0, $2, 'internal', NOW(), $3, 'sent', $4)`,
		ruleID, ruleKey+"|r4", created.Add(-time.Hour), sentAt)
	require.NoError(t, err)

	store := pgAutomationDeliveryStore{pool: pool}
	seen := map[string]int{}
	var cursor *DeliveryCursor
	for page := 0; page < 5; page++ {
		rows, next, err := store.List(ctx, DeliveryFilter{RuleKey: ruleKey, Limit: 2, Cursor: cursor})
		require.NoError(t, err)
		for _, d := range rows {
			seen[d.RecordID]++
		}
		if next == "" {
			break
		}
		cursor, err = decodeDeliveryCursor(next)
		require.NoError(t, err)
	}
	assert.Equal(t, map[string]int{"r1": 1, "r2": 1, "r3": 1, "r4": 1}, seen)

	all, _, err := store.List(ctx, DeliveryFilter{RuleKey: ruleKey, Limit: 10})
	require.NoError(t, err)
	require.Len(t, all, 4)
	for _, d := range all {
		if d.RecordID == "r4" {
			require.NotNil(t, d.SentAt)
			assert.True(t, sentAt.Equal(*d.SentAt), "sentAt %v", d.SentAt)
		} else {
			assert.Nil(t, d.SentAt, d.RecordID)
		}
	}
}
