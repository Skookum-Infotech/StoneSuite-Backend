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

// RefreshTokenByHash looks up an active (not expired, not revoked) refresh
// token by its hash. Returns ErrRefreshTokenReused when the token exists but
// is revoked, and ErrRefreshTokenNotFound when it does not exist or is expired.
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
	if revokedAt != nil {
		return nil, ErrRefreshTokenReused
	}
	if time.Now().After(rec.ExpiresAt) {
		return nil, ErrRefreshTokenNotFound
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
