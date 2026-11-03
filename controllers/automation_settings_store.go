package controllers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Schema defaults for automation_settings, used when the singleton row is
// missing (mirrors automation.loadSettings).
const (
	defaultAutomationDailyExternalCap    = 200
	defaultAutomationPerContactWeeklyCap = 3
)

// AutomationSettings is the per-tenant automation switch and caps as exposed by
// the settings API. PlatformEnabled is nil when the control plane could not be read.
type AutomationSettings struct {
	KillSwitch          bool       `json:"killSwitch"`
	DailyExternalCap    int        `json:"dailyExternalCap"`
	PerContactWeeklyCap int        `json:"perContactWeeklyCap"`
	UpdatedAt           *time.Time `json:"updatedAt"`
	PlatformEnabled     *bool      `json:"platformEnabled"`
}

// automationSettingsStore is the persistence the settings API needs (consumer-side).
type automationSettingsStore interface {
	Get(ctx context.Context) (AutomationSettings, error)
	SetKillSwitch(ctx context.Context, on bool) (AutomationSettings, error)
}

// pgAutomationSettingsStore implements automationSettingsStore on a tenant pool.
type pgAutomationSettingsStore struct{ pool *pgxpool.Pool }

const automationSettingsSelect = `
SELECT kill_switch, daily_external_cap, per_contact_weekly_cap, updated_at
FROM automation_settings WHERE id`

func defaultAutomationSettings() AutomationSettings {
	return AutomationSettings{
		DailyExternalCap:    defaultAutomationDailyExternalCap,
		PerContactWeeklyCap: defaultAutomationPerContactWeeklyCap,
	}
}

// Get returns the singleton row, or the schema defaults when it is absent.
func (s pgAutomationSettingsStore) Get(ctx context.Context) (AutomationSettings, error) {
	out := defaultAutomationSettings()
	var at time.Time
	err := s.pool.QueryRow(ctx, automationSettingsSelect).
		Scan(&out.KillSwitch, &out.DailyExternalCap, &out.PerContactWeeklyCap, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return AutomationSettings{}, fmt.Errorf("load automation settings: %w", err)
	}
	out.UpdatedAt = &at
	return out, nil
}

// SetKillSwitch upserts the singleton and stamps updated_at. Delivery rows are
// untouched: the send guards already defer them as kill_switch.
func (s pgAutomationSettingsStore) SetKillSwitch(ctx context.Context, on bool) (AutomationSettings, error) {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO automation_settings (id, kill_switch) VALUES (TRUE, $1)
		 ON CONFLICT (id) DO UPDATE SET kill_switch = EXCLUDED.kill_switch, updated_at = NOW()`, on); err != nil {
		return AutomationSettings{}, fmt.Errorf("upsert automation kill switch: %w", err)
	}
	return s.Get(ctx)
}
