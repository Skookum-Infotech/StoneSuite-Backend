//go:build dbtest

package workflow

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeWorkflowKey is a throwaway workflow created per test so the numbering
// config mutations here never collide with the seeded document-module
// workflows or another package's dbtests.
const probeWorkflowKey = "numbering_probe_wf"

// setupNumberingProbe creates a throwaway workflow plus a scratch table for
// AssignRecordNumber to write into, so the helper can be exercised without any
// real module's schema. It returns the workflow id and registers cleanup.
func setupNumberingProbe(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()

	_, err := pool.Exec(ctx, `DELETE FROM workflows WHERE LOWER(key) = LOWER($1)`, probeWorkflowKey)
	require.NoError(t, err)
	var wfID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO workflows (key, name, description, enabled, is_default)
		VALUES ($1, 'Numbering Probe', '', TRUE, FALSE)
		RETURNING id`, probeWorkflowKey).Scan(&wfID))

	// probe_number is VARCHAR(20) to match the real module *_number columns, so
	// the over-length path (SQLSTATE 22001) is exercised too.
	_, err = pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS _numbering_probe (
			probe_id     BIGINT PRIMARY KEY,
			probe_number VARCHAR(20),
			CONSTRAINT uq_numbering_probe UNIQUE (probe_number)
		)`)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `TRUNCATE _numbering_probe`)
	require.NoError(t, err)

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DROP TABLE IF EXISTS _numbering_probe`)
		_, _ = pool.Exec(bg, `DELETE FROM workflows WHERE LOWER(key) = LOWER($1)`, probeWorkflowKey)
	})
	return wfID
}

func probeTarget() NumberTarget {
	return NumberTarget{
		WorkflowKey: probeWorkflowKey,
		UpdateSQL:   `UPDATE _numbering_probe SET probe_number = $1 WHERE probe_id = $2`,
		Fallback:    func(id int64) string { return fmt.Sprintf("FB-%06d", id) },
	}
}

func seedProbeRow(t *testing.T, pool *pgxpool.Pool, id int64, number any) {
	t.Helper()
	_, err := pool.Exec(context.Background(),
		`INSERT INTO _numbering_probe (probe_id, probe_number) VALUES ($1, $2)`, id, number)
	require.NoError(t, err)
}

func TestAssignRecordNumber_ConfiguredNumberWhenEnabled(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wfID := setupNumberingProbe(t, pool)

	require.NoError(t, UpsertNumberingConfig(ctx, pool, NumberingConfig{
		WorkflowID: wfID, Enabled: true, Prefix: "Q-", Suffix: "", MinDigits: 5, NextNumber: 42,
	}))
	seedProbeRow(t, pool, 1, nil)

	got, err := AssignRecordNumber(ctx, pool, probeTarget(), 1)
	require.NoError(t, err)
	assert.Equal(t, "Q-00042", got)

	var stored string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT probe_number FROM _numbering_probe WHERE probe_id = 1`).Scan(&stored))
	assert.Equal(t, "Q-00042", stored)

	cfg, err := GetNumberingConfig(ctx, pool, wfID)
	require.NoError(t, err)
	assert.Equal(t, int64(43), cfg.NextNumber, "counter advances after a claim")
}

func TestAssignRecordNumber_FallsBackWhenNoConfigRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	setupNumberingProbe(t, pool)
	seedProbeRow(t, pool, 7, nil)

	got, err := AssignRecordNumber(ctx, pool, probeTarget(), 7)
	require.NoError(t, err)
	assert.Equal(t, "FB-000007", got)

	var stored string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT probe_number FROM _numbering_probe WHERE probe_id = 7`).Scan(&stored))
	assert.Equal(t, "FB-000007", stored)
}

func TestAssignRecordNumber_FallsBackWhenConfigDisabled(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wfID := setupNumberingProbe(t, pool)
	require.NoError(t, UpsertNumberingConfig(ctx, pool, NumberingConfig{
		WorkflowID: wfID, Enabled: false, Prefix: "Q-", MinDigits: 5, NextNumber: 9,
	}))
	seedProbeRow(t, pool, 3, nil)

	got, err := AssignRecordNumber(ctx, pool, probeTarget(), 3)
	require.NoError(t, err)
	assert.Equal(t, "FB-000003", got)
}

func TestAssignRecordNumber_CollisionReturnsErrNumberCollision(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wfID := setupNumberingProbe(t, pool)
	require.NoError(t, UpsertNumberingConfig(ctx, pool, NumberingConfig{
		WorkflowID: wfID, Enabled: true, Prefix: "Q-", MinDigits: 5, NextNumber: 100,
	}))
	seedProbeRow(t, pool, 1, "Q-00100") // an existing record already holds this number
	seedProbeRow(t, pool, 2, nil)

	_, err := AssignRecordNumber(ctx, pool, probeTarget(), 2)
	assert.True(t, errors.Is(err, ErrNumberCollision), "want ErrNumberCollision, got %v", err)
	assert.True(t, IsNumberConfigError(err))
}

func TestAssignRecordNumber_OverLongReturnsErrNumberTooLong(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wfID := setupNumberingProbe(t, pool)
	// prefix (17) + 5 digits + suffix (3) = 25 chars > VARCHAR(20).
	require.NoError(t, UpsertNumberingConfig(ctx, pool, NumberingConfig{
		WorkflowID: wfID, Enabled: true, Prefix: "SUPER-LONG-PREFIX", Suffix: "-US", MinDigits: 5, NextNumber: 1,
	}))
	seedProbeRow(t, pool, 1, nil)

	_, err := AssignRecordNumber(ctx, pool, probeTarget(), 1)
	assert.True(t, errors.Is(err, ErrNumberTooLong), "want ErrNumberTooLong, got %v", err)
	assert.True(t, IsNumberConfigError(err))
}

func TestGenerateRecordNumberByKey_EmptyWhenDisabledOrUnknown(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wfID := setupNumberingProbe(t, pool)

	got, err := GenerateRecordNumberByKey(ctx, pool, "no-such-workflow")
	require.NoError(t, err)
	assert.Equal(t, "", got)

	got, err = GenerateRecordNumberByKey(ctx, pool, probeWorkflowKey)
	require.NoError(t, err)
	assert.Equal(t, "", got)

	require.NoError(t, UpsertNumberingConfig(ctx, pool, NumberingConfig{
		WorkflowID: wfID, Enabled: false, Prefix: "X", MinDigits: 3, NextNumber: 1,
	}))
	got, err = GenerateRecordNumberByKey(ctx, pool, probeWorkflowKey)
	require.NoError(t, err)
	assert.Equal(t, "", got)
}

func TestGenerateRecordNumberByKey_ClaimsWhenEnabled(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wfID := setupNumberingProbe(t, pool)
	require.NoError(t, UpsertNumberingConfig(ctx, pool, NumberingConfig{
		WorkflowID: wfID, Enabled: true, Prefix: "INV-", Suffix: "-A", MinDigits: 4, NextNumber: 7,
	}))

	got, err := GenerateRecordNumberByKey(ctx, pool, probeWorkflowKey)
	require.NoError(t, err)
	assert.Equal(t, "INV-0007-A", got)

	got, err = GenerateRecordNumberByKey(ctx, pool, probeWorkflowKey)
	require.NoError(t, err)
	assert.Equal(t, "INV-0008-A", got)
}
