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
var ErrRefreshTokenReused = errors.New("refresh token already revoked (possible reuse attack)")

// RefreshTokenRecord holds the persisted state for one refresh token.
type RefreshTokenRecord struct {
	ID         string
	IdentityID string
	ExpiresAt  time.Time
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
		return nil, err
	}
	return &rec, nil
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

// RotateRefreshToken inserts the replacement token and revokes the old one in a
// single transaction, so a failed revoke can never leave two live tokens.
func (c *ControlPlane) RotateRefreshToken(ctx context.Context, oldHash, identityID, newHash string, expiresAt time.Time) error {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rotate refresh token: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit
	if _, err := tx.Exec(ctx,
		`INSERT INTO refresh_tokens (identity_id, token_hash, expires_at)
		 VALUES ($1, $2, $3)`, identityID, newHash, expiresAt); err != nil {
		return fmt.Errorf("rotate refresh token: insert: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = NOW()
		 WHERE token_hash = $1 AND revoked_at IS NULL`, oldHash); err != nil {
		return fmt.Errorf("rotate refresh token: revoke old: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("rotate refresh token: commit: %w", err)
	}
	return nil
}

// RevokeAllRefreshTokens revokes every active refresh token for an identity.
// Used on logout to invalidate all sessions across devices.
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
