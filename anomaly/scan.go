package anomaly

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// DB is the tenant database handle Scan and Alert need; *pgxpool.Pool satisfies it.
type DB interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ScanResult counts what one scan did for one tenant.
type ScanResult struct {
	// Raised is how many new findings were stored; Existing how many a rule
	// returned that were stored already (a race with another scan).
	Raised   int
	Existing int
}

// Scan runs every rule and stores each new finding with its recipients. One
// failing rule or finding never stops the others; their errors are joined.
func Scan(ctx context.Context, db DB, rules []Rule, today time.Time) (ScanResult, error) {
	var (
		res  ScanResult
		errs []error
	)
	for _, rule := range rules {
		findings, err := rule.Detect(ctx, db, today)
		if err != nil {
			errs = append(errs, fmt.Errorf("rule %s: %w", rule.Key(), err))
		}
		for _, f := range findings {
			created, err := store(ctx, db, f)
			if err != nil {
				errs = append(errs, fmt.Errorf("rule %s: %w", rule.Key(), err))
				continue
			}
			if created {
				res.Raised++
				slog.WarnContext(ctx, "anomaly detected", "rule", f.RuleKey, "severity", string(f.Severity), "resource", f.Resource, "record_id", f.RecordUUID)
			} else {
				res.Existing++
			}
		}
	}
	return res, errors.Join(errs...)
}

// store validates f, resolves who it goes to, and writes both in one transaction.
func store(ctx context.Context, db DB, f Finding) (bool, error) {
	if err := f.validate(); err != nil {
		return false, fmt.Errorf("invalid finding %s/%s: %w", f.RuleKey, f.SubjectKey, err)
	}
	recipients, err := resolveRecipients(ctx, db, f)
	if err != nil {
		return false, err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	created, err := record(ctx, tx, f, recipients)
	if err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			slog.WarnContext(ctx, "anomaly: rollback failed", "error", rbErr)
		}
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit anomaly %s/%s: %w", f.RuleKey, f.SubjectKey, err)
	}
	return created, nil
}
