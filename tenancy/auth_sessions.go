package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Session statuses as the old browser sees them (GET /api/auth/session).
const (
	// SessionActive is a live session.
	SessionActive = "active"
	// SessionReplacing is a session another sign-in took over; it may still
	// read until its EndsAt while its browser counts down.
	SessionReplacing = "replacing"
	// SessionEnded is a logged-out session, or a replaced one past EndsAt.
	SessionEnded = "ended"
)

// sessionTouchInterval bounds how often last_seen_at is written per session.
const sessionTouchInterval = time.Minute

// ErrSessionNotFound is returned when no session row matches.
var ErrSessionNotFound = errors.New("auth session not found")

// ErrTakeoverTicketNotFound covers a missing, consumed, or expired ticket --
// indistinguishable to the caller deliberately.
var ErrTakeoverTicketNotFound = errors.New("takeover ticket not found")

// AuthSession is one browser sign-in of an identity.
type AuthSession struct {
	ID          string
	IdentityID  string
	CreatedAt   time.Time
	LastSeenAt  time.Time
	DeviceLabel string
	ReplacedAt  *time.Time
	ReplacedBy  string
	EndsAt      *time.Time
	EndedAt     *time.Time
}

// Status classifies the session at instant now (see the Session* constants).
func (s *AuthSession) Status(now time.Time) string {
	return sessionStatus(s.ReplacedAt, s.EndsAt, s.EndedAt, now)
}

// sessionStatus is the pure classification behind AuthSession.Status.
func sessionStatus(replacedAt, endsAt, endedAt *time.Time, now time.Time) string {
	switch {
	case endedAt != nil:
		return SessionEnded
	case replacedAt == nil:
		return SessionActive
	case endsAt != nil && now.Before(*endsAt):
		return SessionReplacing
	default:
		return SessionEnded
	}
}

// NewSessionInput describes the browser a session is created for.
type NewSessionInput struct {
	IdentityID  string
	UserAgent   string
	IP          string
	DeviceLabel string
}

// CreateSession records a new browser sign-in and returns its id.
func (c *ControlPlane) CreateSession(ctx context.Context, in NewSessionInput) (string, error) {
	var id string
	if err := c.pool.QueryRow(ctx,
		`INSERT INTO auth_sessions (identity_id, user_agent, ip, device_label)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		in.IdentityID, in.UserAgent, in.IP, in.DeviceLabel,
	).Scan(&id); err != nil {
		return "", fmt.Errorf("create auth session: %w", err)
	}
	return id, nil
}

const authSessionColumns = `id, identity_id, created_at, last_seen_at, device_label,
	replaced_at, COALESCE(replaced_by::text, ''), ends_at, ended_at`

// scanAuthSession scans one authSessionColumns row.
func scanAuthSession(row pgx.Row) (*AuthSession, error) {
	var s AuthSession
	err := row.Scan(&s.ID, &s.IdentityID, &s.CreatedAt, &s.LastSeenAt, &s.DeviceLabel,
		&s.ReplacedAt, &s.ReplacedBy, &s.EndsAt, &s.EndedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSessionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("scan auth session: %w", err)
	}
	return &s, nil
}

// SessionByID loads one session.
func (c *ControlPlane) SessionByID(ctx context.Context, id string) (*AuthSession, error) {
	return scanAuthSession(c.pool.QueryRow(ctx,
		"SELECT "+authSessionColumns+" FROM auth_sessions WHERE id = $1", id))
}

// LiveSessionFor returns the identity's most recently seen live session
// (neither logged out nor replaced), or ErrSessionNotFound.
func (c *ControlPlane) LiveSessionFor(ctx context.Context, identityID string) (*AuthSession, error) {
	return scanAuthSession(c.pool.QueryRow(ctx,
		"SELECT "+authSessionColumns+` FROM auth_sessions
		 WHERE identity_id = $1 AND ended_at IS NULL AND replaced_at IS NULL
		 ORDER BY last_seen_at DESC LIMIT 1`, identityID))
}

// ReplaceSessions marks every other live session of identityID as replaced by
// keepID, ending after grace. Returns the ids it replaced. Their refresh
// tokens are left alone: RefreshSession refuses any token whose session is not
// active, which keeps the reuse detector (revoke-all) from firing on them.
func (c *ControlPlane) ReplaceSessions(ctx context.Context, identityID, keepID string, grace time.Duration) ([]string, error) {
	rows, err := c.pool.Query(ctx,
		`UPDATE auth_sessions
		 SET replaced_at = NOW(), replaced_by = $2,
		     ends_at = NOW() + $3::bigint * INTERVAL '1 millisecond'
		 WHERE identity_id = $1 AND id <> $2 AND ended_at IS NULL AND replaced_at IS NULL
		 RETURNING id`, identityID, keepID, grace.Milliseconds())
	if err != nil {
		return nil, fmt.Errorf("replace auth sessions: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("replace auth sessions: collect: %w", err)
	}
	return ids, nil
}

// EndSession marks a session logged out. Ending an unknown or already-ended
// session is not an error.
func (c *ControlPlane) EndSession(ctx context.Context, id string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE auth_sessions SET ended_at = NOW() WHERE id = $1 AND ended_at IS NULL`, id); err != nil {
		return fmt.Errorf("end auth session: %w", err)
	}
	return nil
}

// TouchSession records that the session's browser is still in use, writing
// at most once per sessionTouchInterval.
func (c *ControlPlane) TouchSession(ctx context.Context, id string) error {
	if _, err := c.pool.Exec(ctx,
		`UPDATE auth_sessions SET last_seen_at = NOW()
		 WHERE id = $1 AND ended_at IS NULL
		   AND last_seen_at < NOW() - $2::bigint * INTERVAL '1 second'`,
		id, int64(sessionTouchInterval/time.Second)); err != nil {
		return fmt.Errorf("touch auth session: %w", err)
	}
	return nil
}

// CreateTakeoverTicket stores a single-use takeover ticket by its hash, pruning
// the identity's expired ones.
func (c *ControlPlane) CreateTakeoverTicket(ctx context.Context, tokenHash, identityID string, rememberMe bool, ttl time.Duration) error {
	// Prune this identity's expired tickets as new ones are minted, so the
	// table stays bounded without a sweeper.
	if _, err := c.pool.Exec(ctx,
		`DELETE FROM auth_session_takeover_tickets WHERE identity_id = $1 AND expires_at <= NOW()`,
		identityID); err != nil {
		return fmt.Errorf("prune takeover tickets: %w", err)
	}
	if _, err := c.pool.Exec(ctx,
		`INSERT INTO auth_session_takeover_tickets (token_hash, identity_id, remember_me, expires_at)
		 VALUES ($1, $2, $3, $4)`,
		tokenHash, identityID, rememberMe, time.Now().Add(ttl)); err != nil {
		return fmt.Errorf("create takeover ticket: %w", err)
	}
	return nil
}

// ConsumeTakeoverTicket validates and deletes a ticket atomically, returning
// what it was issued for.
func (c *ControlPlane) ConsumeTakeoverTicket(ctx context.Context, tokenHash string) (identityID string, rememberMe bool, err error) {
	err = c.pool.QueryRow(ctx,
		`DELETE FROM auth_session_takeover_tickets
		 WHERE token_hash = $1 AND expires_at > NOW()
		 RETURNING identity_id, remember_me`, tokenHash,
	).Scan(&identityID, &rememberMe)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrTakeoverTicketNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("consume takeover ticket: %w", err)
	}
	return identityID, rememberMe, nil
}

// SetTenantSingleSession turns the single-session rule on or off for a
// customer tenant. The platform-owner tenant is always exempt: turning it on
// there returns ErrPlatformOwnerExempt.
func (c *ControlPlane) SetTenantSingleSession(ctx context.Context, tenantID string, enforced bool) error {
	defer c.forgetTenant(tenantID)
	var isOwner bool
	err := c.pool.QueryRow(ctx,
		`UPDATE tenants SET single_session_enforced = ($2 AND NOT is_platform_owner), updated_at = NOW()
		 WHERE id = $1 RETURNING is_platform_owner`, tenantID, enforced).Scan(&isOwner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrTenantNotFound
	}
	if err != nil {
		return fmt.Errorf("set tenant single session: %w", err)
	}
	if isOwner && enforced {
		return ErrPlatformOwnerExempt
	}
	return nil
}

// ErrPlatformOwnerExempt is returned when a rule the platform-owner tenant is
// always exempt from is switched on for it.
var ErrPlatformOwnerExempt = errors.New("platform-owner tenant is exempt")
