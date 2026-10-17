package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/automation"
)

// ErrAutomationRuleNotFound is returned when no rule has the requested key.
var ErrAutomationRuleNotFound = errors.New("automation rule not found")

// AutomationRule is one automation rule as exposed by the rules API.
type AutomationRule struct {
	RuleKey   string                      `json:"ruleKey"`
	Name      string                      `json:"name"`
	Audience  string                      `json:"audience"`
	Enabled   bool                        `json:"enabled"`
	EnabledAt *time.Time                  `json:"enabledAt"`
	Cadence   []automation.ARCadenceStage `json:"cadence"`
}

// automationRuleStore is the persistence the rules API needs (consumer-side).
type automationRuleStore interface {
	List(ctx context.Context) ([]AutomationRule, error)
	SetEnabled(ctx context.Context, key string, enabled bool) (AutomationRule, error)
}

// pgAutomationRuleStore implements automationRuleStore on a tenant pool.
type pgAutomationRuleStore struct{ pool *pgxpool.Pool }

const automationRuleSelect = `
SELECT r.rule_key, r.name, r.audience, r.enabled, s.enabled_at, r.rule
FROM automation_rules r
LEFT JOIN automation_state s ON s.rule_id = r.id`

// cancelUnsentDeliveriesSQL skips a disabled rule's unsent rows; claimed rows are in flight and left alone.
const cancelUnsentDeliveriesSQL = `
UPDATE automation_deliveries
SET status = 'skipped', reason = $2, lease_until = NULL, updated_at = NOW()
WHERE rule_id = $1 AND status IN ('planned', 'failed')`

func scanAutomationRule(row pgx.Row) (AutomationRule, error) {
	var (
		rule AutomationRule
		raw  []byte
	)
	if err := row.Scan(&rule.RuleKey, &rule.Name, &rule.Audience, &rule.Enabled, &rule.EnabledAt, &raw); err != nil {
		return AutomationRule{}, err
	}
	var cfg automation.ARRuleConfig
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return AutomationRule{}, fmt.Errorf("decode rule config %s: %w", rule.RuleKey, err)
		}
	}
	rule.Cadence = cfg.Cadence
	if rule.Cadence == nil {
		rule.Cadence = []automation.ARCadenceStage{}
	}
	return rule, nil
}

// List returns every automation rule ordered by key.
func (s pgAutomationRuleStore) List(ctx context.Context) ([]AutomationRule, error) {
	rows, err := s.pool.Query(ctx, automationRuleSelect+" ORDER BY r.rule_key")
	if err != nil {
		return nil, fmt.Errorf("list automation rules: %w", err)
	}
	defer rows.Close()
	out := []AutomationRule{}
	for rows.Next() {
		rule, err := scanAutomationRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan automation rule: %w", err)
		}
		out = append(out, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate automation rules: %w", err)
	}
	return out, nil
}

// SetEnabled flips a rule in one transaction; enabled_at is stamped only on a disabled-to-enabled change.
func (s pgAutomationRuleStore) SetEnabled(ctx context.Context, key string, enabled bool) (AutomationRule, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AutomationRule{}, fmt.Errorf("begin rule toggle: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		id   string
		prev bool
	)
	err = tx.QueryRow(ctx,
		`SELECT id, enabled FROM automation_rules WHERE rule_key = $1 FOR UPDATE`, key).Scan(&id, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return AutomationRule{}, ErrAutomationRuleNotFound
	}
	if err != nil {
		return AutomationRule{}, fmt.Errorf("lock automation rule: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE automation_rules SET enabled = $2, updated_at = NOW() WHERE id = $1`, id, enabled); err != nil {
		return AutomationRule{}, fmt.Errorf("update automation rule: %w", err)
	}
	if enabled && !prev {
		if _, err := tx.Exec(ctx,
			`INSERT INTO automation_state (rule_id, enabled_at) VALUES ($1, NOW())
			 ON CONFLICT (rule_id) DO UPDATE SET enabled_at = EXCLUDED.enabled_at`, id); err != nil {
			return AutomationRule{}, fmt.Errorf("stamp automation enabled_at: %w", err)
		}
	}
	if prev && !enabled {
		if _, err := tx.Exec(ctx, cancelUnsentDeliveriesSQL, id, automation.ReasonRuleDisabled); err != nil {
			return AutomationRule{}, fmt.Errorf("skip unsent deliveries: %w", err)
		}
	}
	rule, err := scanAutomationRule(tx.QueryRow(ctx, automationRuleSelect+" WHERE r.id = $1", id))
	if err != nil {
		return AutomationRule{}, fmt.Errorf("reload automation rule: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return AutomationRule{}, fmt.Errorf("commit rule toggle: %w", err)
	}
	return rule, nil
}
