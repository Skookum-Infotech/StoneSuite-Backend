//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/automation"
)

// seedToggleTestRule inserts a disabled non-system rule and removes it (deliveries first) on cleanup.
func seedToggleTestRule(t *testing.T, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	ctx := context.Background()
	key := fmt.Sprintf("test_toggle_%d", time.Now().UnixNano())
	var id string
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO automation_rules (rule_key, name, enabled, is_system, audience, rule)
		 VALUES ($1, 'Toggle test', FALSE, FALSE, 'external', '{}') RETURNING id`, key).Scan(&id))
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM automation_deliveries WHERE rule_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM automation_state WHERE rule_id = $1`, id)
		_, _ = pool.Exec(ctx, `DELETE FROM automation_rules WHERE id = $1`, id)
	})
	return id, key
}

func seedToggleDelivery(t *testing.T, pool *pgxpool.Pool, ruleID, status string) string {
	t.Helper()
	var id string
	var lease *time.Time
	if status == "claimed" {
		l := time.Now().Add(5 * time.Minute)
		lease = &l
	}
	occ := fmt.Sprintf("test-toggle-%s-%d", status, time.Now().UnixNano())
	require.NoError(t, pool.QueryRow(context.Background(),
		`INSERT INTO automation_deliveries
		   (rule_id, resource, record_id, anchor_date, stage, occurrence_key, recipient_kind, run_at, status, lease_until)
		 VALUES ($1, 'invoice', 'rec-1', CURRENT_DATE, 0, $2, 'external', NOW(), $3, $4)
		 RETURNING id`, ruleID, occ, status, lease).Scan(&id))
	return id
}

func TestSetEnabledDisableSkipsUnsentRows(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	ctx := context.Background()
	store := pgAutomationRuleStore{pool: pool}
	ruleID, key := seedToggleTestRule(t, pool)

	_, err := store.SetEnabled(ctx, key, true)
	require.NoError(t, err)

	ids := map[string]string{}
	for _, st := range []string{"planned", "failed", "claimed", "sent", "unknown"} {
		ids[st] = seedToggleDelivery(t, pool, ruleID, st)
	}

	_, err = store.SetEnabled(ctx, key, false)
	require.NoError(t, err)

	for st, id := range ids {
		var status string
		var reason *string
		var lease *time.Time
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT status, reason, lease_until FROM automation_deliveries WHERE id = $1`, id).Scan(&status, &reason, &lease))
		switch st {
		case "planned", "failed":
			assert.Equal(t, "skipped", status, st)
			require.NotNil(t, reason, st)
			assert.Equal(t, automation.ReasonRuleDisabled, *reason, st)
			assert.Nil(t, lease, st)
		default:
			assert.Equal(t, st, status, "%s row must be untouched", st)
			assert.Nil(t, reason, st)
		}
	}
}

func TestSetEnabledEnableStampsEnabledAtOnce(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	ctx := context.Background()
	store := pgAutomationRuleStore{pool: pool}
	_, key := seedToggleTestRule(t, pool)

	first, err := store.SetEnabled(ctx, key, true)
	require.NoError(t, err)
	require.NotNil(t, first.EnabledAt)

	time.Sleep(10 * time.Millisecond)
	again, err := store.SetEnabled(ctx, key, true)
	require.NoError(t, err)
	require.NotNil(t, again.EnabledAt)
	assert.True(t, first.EnabledAt.Equal(*again.EnabledAt), "re-enabling an enabled rule keeps enabled_at")

	_, err = store.SetEnabled(ctx, key, false)
	require.NoError(t, err)
	time.Sleep(10 * time.Millisecond)
	re, err := store.SetEnabled(ctx, key, true)
	require.NoError(t, err)
	require.NotNil(t, re.EnabledAt)
	assert.True(t, re.EnabledAt.After(*first.EnabledAt), "disable then enable resets enabled_at")
}

func TestSetEnabledUnknownKey(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	_, err := pgAutomationRuleStore{pool: pool}.SetEnabled(context.Background(), "no_such_rule_key", true)
	assert.ErrorIs(t, err, ErrAutomationRuleNotFound)
}

func TestSetEnabledReEnableSkipsLeftoverRows(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	ctx := context.Background()
	store := pgAutomationRuleStore{pool: pool}
	ruleID, key := seedToggleTestRule(t, pool)

	ids := map[string]string{}
	for _, st := range []string{"planned", "failed", "claimed", "sent", "unknown"} {
		ids[st] = seedToggleDelivery(t, pool, ruleID, st)
	}
	// A claim whose lease expired (reverted-in-flight leftover from before the disable).
	expired := seedToggleDelivery(t, pool, ruleID, "claimed")
	_, err := pool.Exec(ctx, `UPDATE automation_deliveries SET lease_until = NOW() - INTERVAL '1 minute' WHERE id = $1`, expired)
	require.NoError(t, err)

	_, err = store.SetEnabled(ctx, key, true) // disabled -> enabled
	require.NoError(t, err)

	check := func(label, id, wantStatus string, wantReason *string) {
		var status string
		var reason *string
		var lease *time.Time
		require.NoError(t, pool.QueryRow(ctx,
			`SELECT status, reason, lease_until FROM automation_deliveries WHERE id = $1`, id).Scan(&status, &reason, &lease), label)
		assert.Equal(t, wantStatus, status, label)
		if wantReason == nil {
			assert.Nil(t, reason, label)
			return
		}
		require.NotNil(t, reason, label)
		assert.Equal(t, *wantReason, *reason, label)
		assert.Nil(t, lease, label)
	}
	pre := automation.ReasonPreEnable
	check("planned", ids["planned"], "skipped", &pre)
	check("failed", ids["failed"], "skipped", &pre)
	check("expired claimed", expired, "skipped", &pre)
	check("live claimed", ids["claimed"], "claimed", nil)
	check("sent", ids["sent"], "sent", nil)
	check("unknown", ids["unknown"], "unknown", nil)
}
