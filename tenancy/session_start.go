package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MoveRecency decides which replacements count as moving a sign-in between
// devices (toward a customer's cooldown and daily cap): every confirmed
// takeover, plus any silent replacement of a session used within this long.
// Replacing a browser abandoned for longer is not a move.
const MoveRecency = 2 * time.Hour

// MoveWindow is the window a customer's daily cap counts moves in.
const MoveWindow = 24 * time.Hour

// Codes of a refused move.
const (
	CodeTakeoverCooldown = "takeover_cooldown"
	CodeTakeoverLimit    = "takeover_limit"
)

// ErrLiveSessionExists refuses to adopt a pre-session token while the user
// has a session in use elsewhere: the newer sign-in wins, silently replacing
// it would be a free move past the prompt and the limits.
var ErrLiveSessionExists = errors.New("another session is in use")

// TakeoverLimits are a customer's limits on moving a sign-in; 0 = off.
type TakeoverLimits struct {
	Cooldown time.Duration
	DailyCap int
}

// TakeoverLimitError refuses a move that would break the limits.
type TakeoverLimitError struct {
	Code       string
	RetryAfter time.Duration
	Count      int
}

// Error implements error.
func (e *TakeoverLimitError) Error() string { return "takeover refused: " + e.Code }

// TakeoverRefusal decides whether one more move is allowed given the moves
// in the last MoveWindow; nil when allowed.
func TakeoverRefusal(l TakeoverLimits, h TakeoverHistory, now time.Time) *TakeoverLimitError {
	if l.DailyCap > 0 && h.Count >= l.DailyCap {
		return &TakeoverLimitError{Code: CodeTakeoverLimit, Count: h.Count}
	}
	if l.Cooldown > 0 && h.Last != nil {
		if wait := h.Last.Add(l.Cooldown).Sub(now); wait > 0 {
			return &TakeoverLimitError{Code: CodeTakeoverCooldown, RetryAfter: wait, Count: h.Count}
		}
	}
	return nil
}

// sessionRetention is how long a logged-out or replaced session (with its
// device and IP) is kept before StartSession prunes it.
const sessionRetention = 90 * 24 * time.Hour

// sessionQuerier is what the session statements need: the pool or a
// transaction.
type sessionQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// StartSessionInput is one sign-in's request for a session.
type StartSessionInput struct {
	NewSessionInput
	// Enforced replaces every other live session of the identity (the
	// single-session rule applies); Grace and Reason are then used.
	Enforced bool
	Grace    time.Duration
	Reason   string
	// Limits, when non-zero, refuse the sign-in (a *TakeoverLimitError) if
	// replacing the other sessions would be a move past them. Checked under
	// the per-identity lock, so simultaneous sign-ins cannot overshoot.
	Limits TakeoverLimits
	// RefuseIfLive (legacy adoption) returns ErrLiveSessionExists instead of
	// replacing a session used within MoveRecency.
	RefuseIfLive bool
}

// StartSessionResult is the session created and the sessions it replaced.
type StartSessionResult struct {
	ID       string
	Replaced []string
}

// StartSession creates a session for a sign-in and, when Enforced, replaces
// the identity's other live sessions -- in one transaction holding a
// per-identity advisory lock, so two sign-ins at the same moment are applied
// one after the other and exactly one session ends up live. It also prunes the
// identity's sessions that ended more than sessionRetention ago.
func (c *ControlPlane) StartSession(ctx context.Context, in StartSessionInput) (StartSessionResult, error) {
	var res StartSessionResult
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("start session: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('auth_session:' || $1))`, in.IdentityID); err != nil {
		return res, fmt.Errorf("start session: lock: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM auth_sessions
		 WHERE identity_id = $1
		   AND COALESCE(ended_at, ends_at, replaced_at) < NOW() - $2::bigint * INTERVAL '1 second'`,
		in.IdentityID, int64(sessionRetention/time.Second)); err != nil {
		return res, fmt.Errorf("start session: prune: %w", err)
	}
	if in.Enforced {
		if err := checkMove(ctx, tx, in, time.Now()); err != nil {
			return res, err
		}
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO auth_sessions (identity_id, user_agent, ip, device_label)
		 VALUES ($1, $2, $3, $4) RETURNING id`,
		in.IdentityID, in.UserAgent, in.IP, in.DeviceLabel,
	).Scan(&res.ID); err != nil {
		return res, fmt.Errorf("start session: create: %w", err)
	}
	if in.Enforced {
		if res.Replaced, err = replaceSessions(ctx, tx, in.IdentityID, res.ID, in.Grace, in.Reason); err != nil {
			return res, fmt.Errorf("start session: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("start session: commit: %w", err)
	}
	return res, nil
}

// checkMove refuses a sign-in that would be a move past the limits, or a
// legacy adoption over a session in use (see StartSessionInput). It runs
// inside StartSession's transaction, after the per-identity lock.
func checkMove(ctx context.Context, q sessionQuerier, in StartSessionInput, now time.Time) error {
	var recentLive bool
	if err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM auth_sessions
		   WHERE identity_id = $1 AND ended_at IS NULL AND replaced_at IS NULL
		     AND (last_seen_at >= $2 OR $3))`,
		in.IdentityID, now.Add(-MoveRecency), in.Reason == ReplaceConfirmed,
	).Scan(&recentLive); err != nil {
		return fmt.Errorf("start session: check live: %w", err)
	}
	if !recentLive {
		return nil // nothing in use is replaced: not a move
	}
	if in.RefuseIfLive {
		return ErrLiveSessionExists
	}
	if in.Limits == (TakeoverLimits{}) {
		return nil
	}
	h, err := movesSince(ctx, q, in.IdentityID, now.Add(-MoveWindow))
	if err != nil {
		return err
	}
	if refusal := TakeoverRefusal(in.Limits, h, now); refusal != nil {
		return refusal
	}
	return nil
}

// TakeoverHistory is how often an identity has moved its sign-in between
// devices.
type TakeoverHistory struct {
	// Count is the moves in the window asked for.
	Count int
	// Last is the most recent move, nil when there was none.
	Last *time.Time
}

// MovesSince counts identityID's moves since since (see MoveRecency for what
// counts) and returns when the latest one happened.
func (c *ControlPlane) MovesSince(ctx context.Context, identityID string, since time.Time) (TakeoverHistory, error) {
	return movesSince(ctx, c.pool, identityID, since)
}

// movesSince is MovesSince on any querier. One sign-in replacing several
// sessions is one move (counted by the session that replaced them).
func movesSince(ctx context.Context, q sessionQuerier, identityID string, since time.Time) (TakeoverHistory, error) {
	var h TakeoverHistory
	if err := q.QueryRow(ctx,
		`SELECT COUNT(DISTINCT replaced_by), MAX(replaced_at)
		 FROM auth_sessions
		 WHERE identity_id = $1 AND replaced_at >= $2
		   AND (replaced_reason = $3
		        OR (replaced_reason <> $5
		            AND last_seen_at >= replaced_at - $4::bigint * INTERVAL '1 second'))`,
		identityID, since, ReplaceConfirmed, int64(MoveRecency/time.Second), ReplacePolicy,
	).Scan(&h.Count, &h.Last); err != nil {
		return h, fmt.Errorf("moves since: %w", err)
	}
	return h, nil
}

// SessionsEpoch is when single sessions began here: the first auth session
// ever created, or now when there is none yet (the first boot after release).
// main derives the session guard's legacy-token cutoff from it.
func (c *ControlPlane) SessionsEpoch(ctx context.Context) (time.Time, error) {
	var first *time.Time
	if err := c.pool.QueryRow(ctx, `SELECT MIN(created_at) FROM auth_sessions`).Scan(&first); err != nil {
		return time.Time{}, fmt.Errorf("sessions epoch: %w", err)
	}
	if first == nil {
		return time.Now(), nil
	}
	return *first, nil
}
