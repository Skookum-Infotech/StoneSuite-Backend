package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LoginThrottle is the per-account backoff policy: after Threshold consecutive
// failures, password logins are refused for Base, doubling with every further
// failure up to Max. It never locks permanently, so an attacker can't use it
// to deny a real user their account for longer than Max.
type LoginThrottle struct {
	Threshold int
	Base      time.Duration
	Max       time.Duration
}

// recordLoginFailureSQL bumps the counter and, once it reaches the threshold,
// pushes locked_until out by base * 2^(count - threshold), capped at max.
const recordLoginFailureSQL = `
	UPDATE identities
	SET failed_login_attempts = failed_login_attempts + 1,
	    locked_until = CASE
	        WHEN failed_login_attempts + 1 >= $2 THEN NOW() + LEAST(
	            make_interval(secs => $3::float8 * power(2, LEAST(failed_login_attempts + 1 - $2, 30))),
	            make_interval(secs => $4::float8))
	        ELSE locked_until END
	WHERE id = $1`

// LoginLockedUntil reports whether password login for identityID is currently
// refused, and until when.
func (c *ControlPlane) LoginLockedUntil(ctx context.Context, identityID string) (time.Time, bool, error) {
	var until *time.Time
	err := c.pool.QueryRow(ctx,
		`SELECT locked_until FROM identities WHERE id = $1 AND locked_until > NOW()`, identityID).Scan(&until)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && until == nil) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("login lock lookup: %w", err)
	}
	return *until, true, nil
}

// RecordLoginFailure counts one failed password attempt under policy t.
func (c *ControlPlane) RecordLoginFailure(ctx context.Context, identityID string, t LoginThrottle) error {
	if _, err := c.pool.Exec(ctx, recordLoginFailureSQL,
		identityID, t.Threshold, t.Base.Seconds(), t.Max.Seconds()); err != nil {
		return fmt.Errorf("record login failure: %w", err)
	}
	return nil
}

// ResetLoginFailures clears the backoff after a successful password check.
// It writes only when there is something to clear, so a normal login costs no
// extra row update.
func (c *ControlPlane) ResetLoginFailures(ctx context.Context, identityID string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE identities SET failed_login_attempts = 0, locked_until = NULL
		 WHERE id = $1 AND (failed_login_attempts <> 0 OR locked_until IS NOT NULL)`, identityID); err != nil {
		return fmt.Errorf("reset login failures: %w", err)
	}
	return nil
}
