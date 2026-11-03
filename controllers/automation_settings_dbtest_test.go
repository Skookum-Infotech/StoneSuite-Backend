//go:build dbtest

package controllers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAutomationSettingsStore_UpsertAndDefaults: a missing singleton reads as the
// schema defaults, and the first flip recreates it and stamps updated_at.
func TestAutomationSettingsStore_UpsertAndDefaults(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	ctx := context.Background()
	store := pgAutomationSettingsStore{pool: pool}

	_, err := pool.Exec(ctx, `DELETE FROM automation_settings`)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `INSERT INTO automation_settings (id) VALUES (TRUE) ON CONFLICT (id) DO NOTHING`)
		_, _ = pool.Exec(ctx, `UPDATE automation_settings SET kill_switch = FALSE`)
	})

	got, err := store.Get(ctx)
	require.NoError(t, err)
	assert.Equal(t, defaultAutomationSettings(), got)
	assert.Nil(t, got.UpdatedAt)

	on, err := store.SetKillSwitch(ctx, true)
	require.NoError(t, err)
	assert.True(t, on.KillSwitch)
	require.NotNil(t, on.UpdatedAt)
	assert.Equal(t, defaultAutomationDailyExternalCap, on.DailyExternalCap)

	off, err := store.SetKillSwitch(ctx, false)
	require.NoError(t, err)
	assert.False(t, off.KillSwitch)
	assert.False(t, off.UpdatedAt.Before(*on.UpdatedAt))

	var rows int
	require.NoError(t, pool.QueryRow(ctx, `SELECT COUNT(*) FROM automation_settings`).Scan(&rows))
	assert.Equal(t, 1, rows, "singleton stays a single row")
}
