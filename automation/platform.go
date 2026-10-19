package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PlatformState is the platform-wide automation switch (control plane).
type PlatformState struct {
	Enabled   bool       `json:"enabled"`
	UpdatedBy string     `json:"updatedBy,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// PlatformStore reads and writes the single platform_automation_settings row.
type PlatformStore struct {
	pool *pgxpool.Pool
}

// NewPlatformStore builds a store over the control-plane pool.
func NewPlatformStore(cpPool *pgxpool.Pool) *PlatformStore {
	return &PlatformStore{pool: cpPool}
}

// State returns the current switch. A missing row (pre-migration boot) means
// disabled: automation is opt-in, so the safe default is to do nothing.
func (s *PlatformStore) State(ctx context.Context) (PlatformState, error) {
	var st PlatformState
	var by *string
	var at time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT enabled, updated_by, updated_at FROM platform_automation_settings WHERE id = 1`,
	).Scan(&st.Enabled, &by, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return PlatformState{}, nil
	}
	if err != nil {
		return PlatformState{}, fmt.Errorf("load platform automation settings: %w", err)
	}
	if by != nil {
		st.UpdatedBy = *by
	}
	st.UpdatedAt = &at
	return st, nil
}

// Enabled reports whether platform-wide automation is on.
func (s *PlatformStore) Enabled(ctx context.Context) (bool, error) {
	st, err := s.State(ctx)
	if err != nil {
		return false, err
	}
	return st.Enabled, nil
}

// Set writes the switch. by is the acting platform admin's email, for the
// audit trail; never a password or token.
func (s *PlatformStore) Set(ctx context.Context, enabled bool, by string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO platform_automation_settings (id, enabled, updated_by, updated_at)
		VALUES (1, $1, $2, now())
		ON CONFLICT (id) DO UPDATE SET
			enabled = EXCLUDED.enabled,
			updated_by = EXCLUDED.updated_by,
			updated_at = EXCLUDED.updated_at`,
		enabled, by)
	if err != nil {
		return fmt.Errorf("save platform automation settings: %w", err)
	}
	return nil
}
