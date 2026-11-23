package automation

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RuleKeyDailyDigest is the rule_key of the seeded daily digest rule.
const RuleKeyDailyDigest = "daily_digest"

const (
	// DigestMinHour and DigestMaxHour bound the tenant-local hour a digest may be
	// sent at: the send window is [08:00, 18:00), so the last whole hour is 17.
	DigestMinHour = sendWindowStartHour
	DigestMaxHour = sendWindowEndHour - 1
	// DefaultDigestHour is when a user who has not chosen gets their digest.
	DefaultDigestHour = sendWindowStartHour
)

// ErrInvalidDigestPrefs is returned for preferences that cannot be stored.
var ErrInvalidDigestPrefs = errors.New("invalid digest preferences")

// DigestPrefs is one user's daily digest delivery preferences. A user with no
// stored row has the defaults below: the digest is off until they opt in.
type DigestPrefs struct {
	Enabled bool `json:"enabled"`
	// SendHour is the tenant-local hour (8..17) the digest is sent from.
	SendHour int `json:"sendHour"`
	// The Include flags choose which sections the digest may contain.
	IncludeApprovals bool `json:"includeApprovals"`
	IncludeInvoices  bool `json:"includeInvoices"`
	IncludeExpiring  bool `json:"includeExpiring"`
	// SkipEmpty suppresses a digest that has nothing to report.
	SkipEmpty bool `json:"skipEmpty"`
}

// DefaultDigestPrefs is what a user who never saved preferences has.
func DefaultDigestPrefs() DigestPrefs {
	return DigestPrefs{Enabled: false, SendHour: DefaultDigestHour,
		IncludeApprovals: true, IncludeInvoices: true, IncludeExpiring: true, SkipEmpty: true}
}

// Validate checks the stored invariants: an hour inside the send window, and at
// least one section when the digest is on (an on digest with no sections could
// only ever send an empty email).
func (p DigestPrefs) Validate() error {
	if p.SendHour < DigestMinHour || p.SendHour > DigestMaxHour {
		return fmt.Errorf("%w: sendHour must be between %d and %d", ErrInvalidDigestPrefs, DigestMinHour, DigestMaxHour)
	}
	if p.Enabled && !p.IncludeApprovals && !p.IncludeInvoices && !p.IncludeExpiring {
		return fmt.Errorf("%w: choose at least one section", ErrInvalidDigestPrefs)
	}
	return nil
}

const digestPrefsColumns = `p.enabled, p.send_hour, p.include_approvals, p.include_invoices, p.include_expiring, p.skip_empty`

func scanDigestPrefs(row pgx.Row) (DigestPrefs, error) {
	var p DigestPrefs
	err := row.Scan(&p.Enabled, &p.SendHour, &p.IncludeApprovals, &p.IncludeInvoices, &p.IncludeExpiring, &p.SkipEmpty)
	return p, err
}

// LoadDigestPrefs returns a user's saved preferences, or the defaults when the
// user has none.
func LoadDigestPrefs(ctx context.Context, q Querier, userID string) (DigestPrefs, error) {
	p, err := scanDigestPrefs(q.QueryRow(ctx,
		`SELECT `+digestPrefsColumns+` FROM automation_digest_prefs p WHERE p.user_id = $1::uuid`, userID))
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultDigestPrefs(), nil
	}
	if err != nil {
		return DigestPrefs{}, fmt.Errorf("load digest prefs: %w", err)
	}
	return p, nil
}

// SaveDigestPrefs validates and upserts a user's preferences.
func SaveDigestPrefs(ctx context.Context, q ExecQuerier, userID string, p DigestPrefs) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
INSERT INTO automation_digest_prefs (user_id, enabled, send_hour, include_approvals, include_invoices, include_expiring, skip_empty, updated_at)
VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, NOW())
ON CONFLICT (user_id) DO UPDATE SET enabled = EXCLUDED.enabled, send_hour = EXCLUDED.send_hour,
  include_approvals = EXCLUDED.include_approvals, include_invoices = EXCLUDED.include_invoices,
  include_expiring = EXCLUDED.include_expiring, skip_empty = EXCLUDED.skip_empty, updated_at = NOW()`,
		userID, p.Enabled, p.SendHour, p.IncludeApprovals, p.IncludeInvoices, p.IncludeExpiring, p.SkipEmpty); err != nil {
		return fmt.Errorf("save digest prefs: %w", err)
	}
	return nil
}

// ExecQuerier is a Querier that can also write (satisfied by *pgxpool.Pool and pgx.Tx).
type ExecQuerier interface {
	Querier
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
