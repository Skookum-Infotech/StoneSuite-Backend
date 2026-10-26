package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/mfa"
)

// ErrMFAChallengeNotFound covers a missing, consumed (used, locked out after
// too many wrong codes, or replaced by a newer one) or expired challenge --
// indistinguishable to the caller deliberately.
var ErrMFAChallengeNotFound = errors.New("mfa challenge not found")

// ErrMFASendLimit means the identity was already sent mfa.MaxSendsPerWindow
// codes within mfa.SendWindow.
var ErrMFASendLimit = errors.New("mfa send limit reached")

// ErrMFAResendRefused means a resend lost a race with another send on the same
// challenge: the cooldown or per-challenge cap failed when re-checked under
// the row lock.
var ErrMFAResendRefused = errors.New("mfa resend refused")

// MFAChallenge is one emailed sign-in code awaiting entry (see the
// mfa_challenges table). CodeHash is bcrypt; the raw code only ever exists in
// the email.
type MFAChallenge struct {
	ID         string
	IdentityID string
	Purpose    string
	CodeHash   string
	Attempts   int
	SendCount  int
	RememberMe bool
	LastSentAt time.Time
	ExpiresAt  time.Time
}

const mfaChallengeColumns = `id, identity_id, purpose, code_hash, attempts, send_count, remember_me, last_sent_at, expires_at`

// mfaChallengeLive selects only challenges that can still accept a code.
const mfaChallengeLive = `consumed_at IS NULL AND expires_at > NOW()`

func scanMFAChallenge(row pgx.Row) (*MFAChallenge, error) {
	var ch MFAChallenge
	err := row.Scan(&ch.ID, &ch.IdentityID, &ch.Purpose, &ch.CodeHash, &ch.Attempts,
		&ch.SendCount, &ch.RememberMe, &ch.LastSentAt, &ch.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMFAChallengeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan mfa challenge: %w", err)
	}
	return &ch, nil
}

// lockIdentityForMFA serialises every code send for one identity, so two
// concurrent sends cannot both pass the per-window cap. Each transaction below
// takes this lock before touching that identity's challenge rows, keeping the
// lock order the same everywhere.
func lockIdentityForMFA(ctx context.Context, tx pgx.Tx, identityID string) error {
	var one int
	err := tx.QueryRow(ctx, `SELECT 1 FROM identities WHERE id = $1 FOR UPDATE`, identityID).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrIdentityNotFound
	}
	if err != nil {
		return fmt.Errorf("lock identity for mfa: %w", err)
	}
	return nil
}

// checkMFASendWindow returns ErrMFASendLimit once identityID has been sent
// mfa.MaxSendsPerWindow codes in the last mfa.SendWindow.
func checkMFASendWindow(ctx context.Context, tx pgx.Tx, identityID string) error {
	var sent int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(SUM(send_count), 0) FROM mfa_challenges
		 WHERE identity_id = $1 AND created_at > NOW() - make_interval(secs => $2)`,
		identityID, mfa.SendWindow.Seconds()).Scan(&sent); err != nil {
		return fmt.Errorf("count mfa sends: %w", err)
	}
	if sent >= mfa.MaxSendsPerWindow {
		return ErrMFASendLimit
	}
	return nil
}

// CreateMFAChallenge opens a challenge for a code the caller is about to
// email. tokenHash is mfa.HashChallengeToken of a random token and codeHash is
// mfa.HashCode of the code -- this method only stores them. Any still-live
// challenge of the same purpose for the identity is retired, and the
// identity's rows older than mfa.ChallengeRetention are pruned. Returns
// ErrMFASendLimit when the identity has had too many codes recently.
func (c *ControlPlane) CreateMFAChallenge(ctx context.Context, identityID, purpose, tokenHash, codeHash string, rememberMe bool) (*MFAChallenge, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create mfa challenge: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	if err := lockIdentityForMFA(ctx, tx, identityID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM mfa_challenges WHERE identity_id = $1 AND created_at < NOW() - make_interval(secs => $2)`,
		identityID, mfa.ChallengeRetention.Seconds()); err != nil {
		return nil, fmt.Errorf("create mfa challenge: prune: %w", err)
	}
	if err := checkMFASendWindow(ctx, tx, identityID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE mfa_challenges SET consumed_at = NOW()
		 WHERE identity_id = $1 AND purpose = $2 AND consumed_at IS NULL`,
		identityID, purpose); err != nil {
		return nil, fmt.Errorf("create mfa challenge: retire previous: %w", err)
	}
	ch, err := scanMFAChallenge(tx.QueryRow(ctx,
		`INSERT INTO mfa_challenges (identity_id, purpose, token_hash, code_hash, remember_me, expires_at)
		 VALUES ($1, $2, $3, $4, $5, NOW() + make_interval(secs => $6))
		 RETURNING `+mfaChallengeColumns,
		identityID, purpose, tokenHash, codeHash, rememberMe, mfa.CodeTTL.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("create mfa challenge: insert: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create mfa challenge: commit: %w", err)
	}
	return ch, nil
}

// MFAChallengeByToken loads a live challenge by its token hash. Returns
// ErrMFAChallengeNotFound when there is none of that purpose.
func (c *ControlPlane) MFAChallengeByToken(ctx context.Context, tokenHash, purpose string) (*MFAChallenge, error) {
	return scanMFAChallenge(c.pool.QueryRow(ctx,
		`SELECT `+mfaChallengeColumns+` FROM mfa_challenges
		 WHERE token_hash = $1 AND purpose = $2 AND `+mfaChallengeLive,
		tokenHash, purpose))
}

// LatestMFAChallenge loads the identity's newest live challenge of purpose,
// for flows where the caller is already signed in and holds no token.
func (c *ControlPlane) LatestMFAChallenge(ctx context.Context, identityID, purpose string) (*MFAChallenge, error) {
	return scanMFAChallenge(c.pool.QueryRow(ctx,
		`SELECT `+mfaChallengeColumns+` FROM mfa_challenges
		 WHERE identity_id = $1 AND purpose = $2 AND `+mfaChallengeLive+`
		 ORDER BY created_at DESC LIMIT 1`,
		identityID, purpose))
}

// RecordMFAFailedAttempt counts one wrong code against a challenge and
// returns its new attempt count. The challenge dies once that reaches
// mfa.MaxAttempts. Returns ErrMFAChallengeNotFound when it was already dead.
func (c *ControlPlane) RecordMFAFailedAttempt(ctx context.Context, challengeID string) (int, error) {
	var attempts int
	err := c.pool.QueryRow(ctx,
		`UPDATE mfa_challenges
		 SET attempts = attempts + 1,
		     consumed_at = CASE WHEN attempts + 1 >= $2 THEN NOW() ELSE NULL END
		 WHERE id = $1 AND consumed_at IS NULL
		 RETURNING attempts`,
		challengeID, mfa.MaxAttempts).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrMFAChallengeNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("record mfa failed attempt: %w", err)
	}
	return attempts, nil
}

// ConsumeMFAChallenge marks a challenge used. Only one caller can win: a
// second consume, or one after expiry, returns ErrMFAChallengeNotFound.
func (c *ControlPlane) ConsumeMFAChallenge(ctx context.Context, challengeID string) error {
	tag, err := c.pool.Exec(ctx,
		`UPDATE mfa_challenges SET consumed_at = NOW() WHERE id = $1 AND `+mfaChallengeLive,
		challengeID)
	if err != nil {
		return fmt.Errorf("consume mfa challenge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrMFAChallengeNotFound
	}
	return nil
}

// ResendMFAChallenge swaps a live challenge's code for a new one (codeHash)
// and restarts its expiry. Wrong-code attempts are not reset. Returns
// ErrMFAChallengeNotFound when the challenge is dead, ErrMFASendLimit when
// the identity's window cap is reached, and ErrMFAResendRefused when the
// cooldown or per-challenge cap fails under the lock (callers check both with
// mfa.CheckResend first, for a precise message).
func (c *ControlPlane) ResendMFAChallenge(ctx context.Context, challengeID, codeHash string) (*MFAChallenge, error) {
	var identityID string
	err := c.pool.QueryRow(ctx,
		`SELECT identity_id FROM mfa_challenges WHERE id = $1 AND `+mfaChallengeLive,
		challengeID).Scan(&identityID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrMFAChallengeNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resend mfa challenge: lookup: %w", err)
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("resend mfa challenge: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	if err := lockIdentityForMFA(ctx, tx, identityID); err != nil {
		return nil, err
	}
	if err := checkMFASendWindow(ctx, tx, identityID); err != nil {
		return nil, err
	}
	ch, err := scanMFAChallenge(tx.QueryRow(ctx,
		`UPDATE mfa_challenges
		 SET code_hash = $2, send_count = send_count + 1, last_sent_at = NOW(),
		     expires_at = NOW() + make_interval(secs => $3)
		 WHERE id = $1 AND `+mfaChallengeLive+`
		   AND send_count < $4
		   AND last_sent_at <= NOW() - make_interval(secs => $5)
		 RETURNING `+mfaChallengeColumns,
		challengeID, codeHash, mfa.CodeTTL.Seconds(), mfa.MaxSendsPerChallenge, mfa.ResendCooldown.Seconds()))
	if errors.Is(err, ErrMFAChallengeNotFound) {
		return nil, ErrMFAResendRefused
	}
	if err != nil {
		return nil, fmt.Errorf("resend mfa challenge: update: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("resend mfa challenge: commit: %w", err)
	}
	return ch, nil
}
