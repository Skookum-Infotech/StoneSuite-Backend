package tenancy

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrRefreshTokenNotFound is returned when the token does not exist, is expired,
// or has already been revoked.
var ErrRefreshTokenNotFound = errors.New("refresh token not found or expired")

// ErrRefreshTokenReused is returned when a token that was already revoked is
// presented again — this indicates a possible token theft / replay attack.
// The concrete error is a *RefreshTokenReusedError naming the identity, so the
// caller can revoke every session that identity holds.
var ErrRefreshTokenReused = errors.New("refresh token already revoked (possible reuse attack)")

// ErrRefreshTokenSuperseded is returned when a token replayed inside
// RefreshRotationGrace has no live successor left to hand over: its successor
// was already rotated or revoked (logout, password change, deactivation).
// Refusing it keeps one live token per session instead of forking a new one.
var ErrRefreshTokenSuperseded = errors.New("refresh token superseded")

// RefreshTokenReusedError is the ErrRefreshTokenReused value, carrying the
// identity the reused token belonged to.
type RefreshTokenReusedError struct {
	IdentityID string
}

// Error implements error.
func (e *RefreshTokenReusedError) Error() string { return ErrRefreshTokenReused.Error() }

// Is makes errors.Is(err, ErrRefreshTokenReused) match.
func (e *RefreshTokenReusedError) Is(target error) bool { return target == ErrRefreshTokenReused }

// RefreshTokenRecord holds the persisted state for one refresh token.
type RefreshTokenRecord struct {
	ID         string
	IdentityID string
	ExpiresAt  time.Time
	// GraceReplay is set by RotateRefreshToken when the rotated token had
	// already been rotated and its successor was replaced (see there).
	GraceReplay bool
}

// HashRefreshToken returns the SHA-256 hex digest of the raw token value.
// Only the hash is stored in the database; the raw value lives in the cookie.
func HashRefreshToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return fmt.Sprintf("%x", sum)
}

// CreateRefreshToken persists a new refresh token. tokenHash must be the
// SHA-256 hex digest of the raw token (use HashRefreshToken).
func (c *ControlPlane) CreateRefreshToken(ctx context.Context, identityID, tokenHash string, expiresAt time.Time) error {
	_, err := c.pool.Exec(ctx,
		`INSERT INTO refresh_tokens (identity_id, token_hash, expires_at)
		 VALUES ($1, $2, $3)`,
		identityID, tokenHash, expiresAt,
	)
	if err != nil {
		return fmt.Errorf("create refresh token: %w", err)
	}
	return nil
}

// RefreshRotationGrace is how long a just-rotated refresh token is still
// honoured. Rotation revokes the old token, but the browser may legitimately
// present it again right after: two tabs reloading together, restored tabs, or
// a retry after the first response was lost. Without a grace window the second
// caller looks like token theft and its cookies are cleared — a logout.
const RefreshRotationGrace = 30 * time.Second

// refreshTokenState classifies a stored refresh token at instant now: nil when
// it may be used, ErrRefreshTokenReused when it was revoked longer ago than
// RefreshRotationGrace, ErrRefreshTokenNotFound when it is expired.
func refreshTokenState(revokedAt *time.Time, expiresAt, now time.Time) error {
	if revokedAt != nil && now.Sub(*revokedAt) > RefreshRotationGrace {
		return ErrRefreshTokenReused
	}
	if now.After(expiresAt) {
		return ErrRefreshTokenNotFound
	}
	return nil
}

// RefreshTokenByHash looks up a usable refresh token by its hash. A token
// revoked within RefreshRotationGrace still counts as usable. Returns
// ErrRefreshTokenReused when it was revoked earlier than that, and
// ErrRefreshTokenNotFound when it does not exist or is expired. Any other
// error is a lookup failure and says nothing about the token's validity.
func (c *ControlPlane) RefreshTokenByHash(ctx context.Context, tokenHash string) (*RefreshTokenRecord, error) {
	var rec RefreshTokenRecord
	var revokedAt *time.Time
	err := c.pool.QueryRow(ctx,
		`SELECT id, identity_id, expires_at, revoked_at
		 FROM refresh_tokens
		 WHERE token_hash = $1`,
		tokenHash,
	).Scan(&rec.ID, &rec.IdentityID, &rec.ExpiresAt, &revokedAt)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRefreshTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup refresh token: %w", err)
	}
	if err := refreshTokenState(revokedAt, rec.ExpiresAt, time.Now()); err != nil {
		return nil, withIdentity(err, rec.IdentityID)
	}
	return &rec, nil
}

// withIdentity attaches identityID to a reuse error so the caller can revoke
// the identity's sessions; other errors pass through unchanged.
func withIdentity(err error, identityID string) error {
	if errors.Is(err, ErrRefreshTokenReused) {
		return &RefreshTokenReusedError{IdentityID: identityID}
	}
	return err
}

// RevokeRefreshToken marks a single token as revoked by its hash.
func (c *ControlPlane) RevokeRefreshToken(ctx context.Context, tokenHash string) error {
	_, err := c.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW()
		 WHERE token_hash = $1 AND revoked_at IS NULL`,
		tokenHash,
	)
	if err != nil {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}

// RevokeRefreshTokenNow ends a session for good: it revokes the token with a
// timestamp already past RefreshRotationGrace, so the rotation grace window
// never applies and the token cannot be refreshed again. Used on logout; a
// plain RevokeRefreshToken (rotation) deliberately keeps the grace.
func (c *ControlPlane) RevokeRefreshTokenNow(ctx context.Context, tokenHash string) error {
	cutoff := time.Now().Add(-(RefreshRotationGrace + time.Second))
	_, err := c.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2
		 WHERE token_hash = $1 AND (revoked_at IS NULL OR revoked_at > $2)`,
		tokenHash, cutoff,
	)
	if err != nil {
		return fmt.Errorf("revoke refresh token now: %w", err)
	}
	return nil
}

// rotationLifetime is the lifetime to give a rotated token: the lifetime the
// token it replaces was issued with (so a remember-me session stays a
// remember-me session), or fallback when that is non-positive or longer than
// max (a row older than the current configuration).
func rotationLifetime(stored, fallback, max time.Duration) time.Duration {
	stored = stored.Round(time.Second)
	if stored <= 0 || stored > max {
		return fallback
	}
	return stored
}

// RotateRefreshToken replaces the token hashed oldHash with one hashed newHash
// and returns the new token's record. It runs in one transaction holding the
// old row's lock, so concurrent refreshes of the same token serialize.
//
// The new token keeps the old one's lifetime (see rotationLifetime). The
// rotated token and its successor share a timestamp — the transaction's NOW()
// is both the old row's revoked_at and the new row's created_at — which is how
// a replay finds the successor without a parent column.
//
// A replay inside RefreshRotationGrace (two tabs, a lost response) cannot be
// handed the successor itself: only its hash is stored. Instead the live
// successor is revoked and replaced by this new token, created with the same
// shared timestamp, so the session still has exactly one live token. A replay
// whose successor is no longer live returns ErrRefreshTokenSuperseded; a
// replay past the grace returns *RefreshTokenReusedError.
func (c *ControlPlane) RotateRefreshToken(ctx context.Context, oldHash, newHash string, fallback, max time.Duration) (*RefreshTokenRecord, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("rotate refresh token: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	var identityID string
	var expiresAt, createdAt time.Time
	var revokedAt *time.Time
	err = tx.QueryRow(ctx,
		`SELECT identity_id, expires_at, created_at, revoked_at
		 FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE`, oldHash,
	).Scan(&identityID, &expiresAt, &createdAt, &revokedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRefreshTokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("rotate refresh token: lock old: %w", err)
	}
	if err := refreshTokenState(revokedAt, expiresAt, time.Now()); err != nil {
		return nil, withIdentity(err, identityID)
	}
	lifetime := rotationLifetime(expiresAt.Sub(createdAt), fallback, max)

	rec := RefreshTokenRecord{IdentityID: identityID}
	if revokedAt == nil {
		if err := tx.QueryRow(ctx,
			`INSERT INTO refresh_tokens (identity_id, token_hash, expires_at)
			 VALUES ($1, $2, NOW() + $3::bigint * INTERVAL '1 second')
			 RETURNING id, expires_at`, identityID, newHash, int64(lifetime/time.Second),
		).Scan(&rec.ID, &rec.ExpiresAt); err != nil {
			return nil, fmt.Errorf("rotate refresh token: insert: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE refresh_tokens SET revoked_at = NOW() WHERE token_hash = $1`, oldHash); err != nil {
			return nil, fmt.Errorf("rotate refresh token: revoke old: %w", err)
		}
	} else {
		tag, err := tx.Exec(ctx,
			`UPDATE refresh_tokens SET revoked_at = NOW()
			 WHERE identity_id = $1 AND created_at = $2 AND revoked_at IS NULL`,
			identityID, *revokedAt)
		if err != nil {
			return nil, fmt.Errorf("rotate refresh token: revoke successor: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil, ErrRefreshTokenSuperseded
		}
		rec.GraceReplay = true
		rec.ExpiresAt = revokedAt.Add(lifetime)
		if err := tx.QueryRow(ctx,
			`INSERT INTO refresh_tokens (identity_id, token_hash, expires_at, created_at)
			 VALUES ($1, $2, $3, $4)
			 RETURNING id`, identityID, newHash, rec.ExpiresAt, *revokedAt,
		).Scan(&rec.ID); err != nil {
			return nil, fmt.Errorf("rotate refresh token: insert replacement: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("rotate refresh token: commit: %w", err)
	}
	return &rec, nil
}

// RevokeAllRefreshTokens revokes every active refresh token for an identity:
// every session on every device ends. Used on token reuse, deactivation and
// suspension. Tokens revoked here have no live successor, so a replay inside
// RefreshRotationGrace is refused by RotateRefreshToken too.
func (c *ControlPlane) RevokeAllRefreshTokens(ctx context.Context, identityID string) error {
	_, err := c.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW()
		 WHERE identity_id = $1 AND revoked_at IS NULL`,
		identityID,
	)
	if err != nil {
		return fmt.Errorf("revoke all refresh tokens for identity %s: %w", identityID, err)
	}
	return nil
}
