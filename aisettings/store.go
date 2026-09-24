// Package aisettings stores and caches the two on/off switches that gate the
// AI assistant: a platform-admin master switch (control plane, one row for
// the whole environment) and a per-tenant switch (tenant DB, one row per
// tenant). The assistant is available only when both are TRUE — see
// Available. Both default TRUE, so a missing row (pre-migration, or a
// tenant that never touched its settings) is treated as enabled.
package aisettings

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PlatformEnabled reports the control-plane master switch. A missing row
// (pre-migration boot, or the seed insert somehow not applied) means
// enabled — the switch defaults to not changing current behaviour. A nil
// cpPool (tests that construct a handler without a control-plane pool, e.g.
// the count-direct dispatch route which never touches it) is treated the
// same way: enabled, no query attempted.
func PlatformEnabled(ctx context.Context, cpPool *pgxpool.Pool) (bool, error) {
	if cpPool == nil {
		return true, nil
	}
	var enabled bool
	err := cpPool.QueryRow(ctx, `SELECT enabled FROM platform_ai_settings WHERE id = 1`).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("load platform ai settings: %w", err)
	}
	return enabled, nil
}

// SetPlatformEnabled writes the control-plane master switch. by identifies
// the acting platform admin (their email) for the audit trail; never a
// password or token.
func SetPlatformEnabled(ctx context.Context, cpPool *pgxpool.Pool, enabled bool, by string) error {
	_, err := cpPool.Exec(ctx, `
		INSERT INTO platform_ai_settings (id, enabled, updated_by, updated_at)
		VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			updated_by = EXCLUDED.updated_by,
			updated_at = EXCLUDED.updated_at`,
		enabled, by)
	if err != nil {
		return fmt.Errorf("save platform ai settings: %w", err)
	}
	return nil
}

// TenantEnabled reports this tenant database's own switch. A missing row
// means enabled, same rationale as PlatformEnabled. A nil tenantPool is
// treated the same way: enabled, no query attempted.
func TenantEnabled(ctx context.Context, tenantPool *pgxpool.Pool) (bool, error) {
	if tenantPool == nil {
		return true, nil
	}
	var enabled bool
	err := tenantPool.QueryRow(ctx, `SELECT assistant_enabled FROM ai_settings WHERE id = 1`).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("load tenant ai settings: %w", err)
	}
	return enabled, nil
}

// SetTenantEnabled writes this tenant database's own switch. by identifies
// the acting user (their email) for the audit trail.
func SetTenantEnabled(ctx context.Context, tenantPool *pgxpool.Pool, enabled bool, by string) error {
	_, err := tenantPool.Exec(ctx, `
		INSERT INTO ai_settings (id, assistant_enabled, updated_by, updated_at)
		VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE SET
			assistant_enabled = EXCLUDED.assistant_enabled,
			updated_by = EXCLUDED.updated_by,
			updated_at = EXCLUDED.updated_at`,
		enabled, by)
	if err != nil {
		return fmt.Errorf("save tenant ai settings: %w", err)
	}
	return nil
}
