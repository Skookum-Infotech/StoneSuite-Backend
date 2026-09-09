package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// GenerateRecordNumber atomically claims and formats the next record number
// for workflowID, as part of an in-flight create-record transaction. Returns
// "" if numbering is not configured or not enabled for this workflow. Used by
// both the v1 JSONB engine (this package's CreateRecord) and the v2 relational
// CRM store (crmstore.relationalStore), which share workflow_numbering_configs.
func GenerateRecordNumber(ctx context.Context, tx Querier, workflowID string) (string, error) {
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

// AssignRecordNumber returns one of these when the tenant's Record Numbering
// config can't produce a usable number for the row. A module surfaces them as
// its own ClientError (see IsNumberConfigError / NumberConfigMessage) rather
// than letting a raw DB error 500.
var (
	// ErrNumberCollision: the generated number is already held by another record.
	ErrNumberCollision = errors.New("configured record number already exists")
	// ErrNumberTooLong: the generated number overflows the module's number
	// column (VARCHAR(20)) — the prefix/suffix are too long.
	ErrNumberTooLong = errors.New("configured record number is too long")
)

// IsNumberConfigError reports whether err is a Record Numbering misconfiguration
// (collision or over-length) that a module should surface as a ClientError.
func IsNumberConfigError(err error) bool {
	return errors.Is(err, ErrNumberCollision) || errors.Is(err, ErrNumberTooLong)
}

// NumberConfigMessage returns the user-facing guidance for a numbering error
// from AssignRecordNumber.
func NumberConfigMessage(err error) string {
	if errors.Is(err, ErrNumberTooLong) {
		return "That Record Numbering configuration produces a number longer than 20 characters. Shorten the prefix or suffix in Configure → Record Numbering."
	}
	return `That Record Numbering configuration would produce a number that already exists. Raise the "Next #" value in Configure → Record Numbering and try again.`
}

// NumberTarget wires one relational document module into the per-workflow
// record-numbering config (workflow_numbering_configs). Its string fields must
// be compile-time constants — UpdateSQL is executed verbatim.
type NumberTarget struct {
	// WorkflowKey is the module's workflows.key, e.g. "quote" or "vendor".
	WorkflowKey string
	// UpdateSQL sets the module's own number column, taking the number as $1
	// and the row's serial id as $2, e.g.
	// `UPDATE quote SET quote_number = $1 WHERE quote_id = $2`.
	UpdateSQL string
	// Fallback formats the module's default number from the row's serial id.
	// Used when numbering is disabled or unconfigured for WorkflowKey.
	Fallback func(serialID int64) string
}

// GenerateRecordNumberByKey is GenerateRecordNumber for callers that hold a
// workflows.key rather than a workflow id (the relational document modules).
// It atomically claims the next configured number in a single statement and
// returns "" when numbering is disabled or unconfigured for the workflow, or
// the key is unknown — the caller then keeps its own default format.
func GenerateRecordNumberByKey(ctx context.Context, tx Querier, workflowKey string) (string, error) {
	var (
		n         int64
		prefix    string
		suffix    string
		minDigits int
	)
	err := tx.QueryRow(ctx, `
		UPDATE workflow_numbering_configs nc
		   SET next_number = next_number + 1, updated_at = NOW()
		  FROM workflows w
		 WHERE w.id = nc.workflow_id
		   AND LOWER(w.key) = LOWER($1)
		   AND nc.enabled = TRUE
		RETURNING nc.next_number - 1, nc.prefix, nc.suffix, nc.min_digits`,
		workflowKey).Scan(&n, &prefix, &suffix, &minDigits)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("generate record number for %q: %w", workflowKey, err)
	}
	return formatRecordNumber(prefix, suffix, minDigits, n), nil
}

// AssignRecordNumber claims target's next configured record number (or uses
// target.Fallback when numbering is disabled/unconfigured for target.WorkflowKey),
// writes it via target.UpdateSQL for the row identified by serialID, and returns
// the number written. It must run inside the caller's create/convert
// transaction so a later failure rolls the counter increment back. A write that
// fails because the configured number collides with an existing record, or is
// too long for the column, is returned wrapped in ErrNumberCollision /
// ErrNumberTooLong.
func AssignRecordNumber(ctx context.Context, tx Querier, target NumberTarget, serialID int64) (string, error) {
	number, err := GenerateRecordNumberByKey(ctx, tx, target.WorkflowKey)
	if err != nil {
		return "", err
	}
	if number == "" {
		number = target.Fallback(serialID)
	}
	if _, err := tx.Exec(ctx, target.UpdateSQL, number, serialID); err != nil {
		switch pgErrCode(err) {
		case "23505": // unique_violation
			return "", fmt.Errorf("%w: %s", ErrNumberCollision, number)
		case "22001": // string_data_right_truncation
			return "", fmt.Errorf("%w: %s", ErrNumberTooLong, number)
		}
		return "", fmt.Errorf("write record number: %w", err)
	}
	return number, nil
}

// pgErrCode returns the SQLSTATE of a PostgreSQL error, or "" if err is not one.
func pgErrCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}
