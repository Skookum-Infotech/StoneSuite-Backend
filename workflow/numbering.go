package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Bounds for a workflow's record-numbering configuration.
const (
	MinDigitsFloor = 1  // smallest allowed zero-pad width
	MinDigitsCeil  = 10 // largest allowed zero-pad width
	MaxAffixLength = 20 // max length of prefix/suffix
)

// NumberingConfig describes how record numbers (e.g. "LEAD-0001") are
// generated for one workflow. When Enabled is false, records created in this
// workflow get no record number.
type NumberingConfig struct {
	WorkflowID string `json:"workflowId"`
	Enabled    bool   `json:"enabled"`
	Prefix     string `json:"prefix"`
	Suffix     string `json:"suffix"`
	MinDigits  int    `json:"minDigits"`
	NextNumber int64  `json:"nextNumber"`
}

// ValidateNumberingConfig checks a config's bounds before it is persisted.
func ValidateNumberingConfig(cfg NumberingConfig) error {
	var errs ValidationErrors
	if cfg.MinDigits < MinDigitsFloor || cfg.MinDigits > MinDigitsCeil {
		errs = append(errs, ValidationError{
			Field:   "minDigits",
			Message: fmt.Sprintf("must be between %d and %d", MinDigitsFloor, MinDigitsCeil),
		})
	}
	if cfg.NextNumber < 1 {
		errs = append(errs, ValidationError{Field: "nextNumber", Message: "must be at least 1"})
	}
	if len(cfg.Prefix) > MaxAffixLength {
		errs = append(errs, ValidationError{Field: "prefix", Message: fmt.Sprintf("must be at most %d characters", MaxAffixLength)})
	}
	if len(cfg.Suffix) > MaxAffixLength {
		errs = append(errs, ValidationError{Field: "suffix", Message: fmt.Sprintf("must be at most %d characters", MaxAffixLength)})
	}
	if len(errs) > 0 {
		return errs
	}
	return nil
}

// GetNumberingConfig loads the numbering configuration for a workflow. If no
// configuration has been saved yet, it returns the defaults (numbering off).
func GetNumberingConfig(ctx context.Context, q Querier, workflowID string) (*NumberingConfig, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workflows WHERE id = $1)`, workflowID).Scan(&exists); err != nil {
		return nil, fmt.Errorf("check workflow exists: %w", err)
	}
	if !exists {
		return nil, ErrWorkflowNotFound
	}

	cfg := &NumberingConfig{
		WorkflowID: workflowID,
		MinDigits:  MinDigitsFloor,
		NextNumber: 1,
	}
	err := q.QueryRow(ctx, `
		SELECT enabled, prefix, suffix, min_digits, next_number
		FROM workflow_numbering_configs WHERE workflow_id = $1`, workflowID).
		Scan(&cfg.Enabled, &cfg.Prefix, &cfg.Suffix, &cfg.MinDigits, &cfg.NextNumber)
	if errors.Is(err, pgx.ErrNoRows) {
		return cfg, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get numbering config: %w", err)
	}
	return cfg, nil
}

// UpsertNumberingConfig validates and saves a workflow's numbering configuration.
func UpsertNumberingConfig(ctx context.Context, q Querier, cfg NumberingConfig) error {
	if err := ValidateNumberingConfig(cfg); err != nil {
		return err
	}
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM workflows WHERE id = $1)`, cfg.WorkflowID).Scan(&exists); err != nil {
		return fmt.Errorf("check workflow exists: %w", err)
	}
	if !exists {
		return ErrWorkflowNotFound
	}
	_, err := q.Exec(ctx, `
		INSERT INTO workflow_numbering_configs (workflow_id, enabled, prefix, suffix, min_digits, next_number)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (workflow_id) DO UPDATE
			SET enabled = EXCLUDED.enabled,
			    prefix = EXCLUDED.prefix,
			    suffix = EXCLUDED.suffix,
			    min_digits = EXCLUDED.min_digits,
			    next_number = EXCLUDED.next_number,
			    updated_at = NOW()`,
		cfg.WorkflowID, cfg.Enabled, cfg.Prefix, cfg.Suffix, cfg.MinDigits, cfg.NextNumber)
	if err != nil {
		return fmt.Errorf("upsert numbering config: %w", err)
	}
	return nil
}

// generateRecordNumber atomically claims and formats the next record number
// for workflowID, as part of an in-flight create-record transaction. Returns
// "" if numbering is not configured or not enabled for this workflow.
func generateRecordNumber(ctx context.Context, tx Querier, workflowID string) (string, error) {
	var (
		n         int64
		prefix    string
		suffix    string
		minDigits int
	)
	err := tx.QueryRow(ctx, `
		UPDATE workflow_numbering_configs
		   SET next_number = next_number + 1, updated_at = NOW()
		 WHERE workflow_id = $1 AND enabled = TRUE
		RETURNING next_number - 1, prefix, suffix, min_digits`, workflowID).
		Scan(&n, &prefix, &suffix, &minDigits)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("generate record number: %w", err)
	}
	return formatRecordNumber(prefix, suffix, minDigits, n), nil
}

// formatRecordNumber renders a record number as prefix + zero-padded sequence + suffix.
func formatRecordNumber(prefix, suffix string, minDigits int, n int64) string {
	return fmt.Sprintf("%s%0*d%s", prefix, minDigits, n, suffix)
}
