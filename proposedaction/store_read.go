package proposedaction

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type listCursor struct {
	CreatedAt time.Time `json:"t"`
	ID        string    `json:"i"`
}

func encodeCursor(c listCursor) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("marshal cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func decodeCursor(s string) (listCursor, error) {
	var c listCursor
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return c, ErrInvalidCursor
	}
	if err := json.Unmarshal(b, &c); err != nil || c.CreatedAt.IsZero() || !uuidPattern.MatchString(c.ID) {
		return listCursor{}, ErrInvalidCursor
	}
	return c, nil
}

func clampLimit(n int) int {
	switch {
	case n <= 0:
		return defaultListLimit
	case n > maxListLimit:
		return maxListLimit
	}
	return n
}

// List returns the caller's own live pending proposals, newest first, with an
// opaque keyset cursor for the next page ("" when there is none).
func (s *Store) List(ctx context.Context, identityID string, limit int, cursor string) ([]Action, string, error) {
	limit = clampLimit(limit)
	args := []any{identityID}
	where := `identity_id = $1 AND status = 'pending' AND expires_at > now()`
	if cursor != "" {
		c, err := decodeCursor(cursor)
		if err != nil {
			return nil, "", err
		}
		args = append(args, c.CreatedAt, c.ID)
		where += ` AND (created_at, id) < ($2, $3::uuid)`
	}
	args = append(args, limit+1)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM ai_proposed_action WHERE %s
		ORDER BY created_at DESC, id DESC LIMIT $%d`, actionColumns, where, len(args)), args...)
	if err != nil {
		return nil, "", fmt.Errorf("list proposed actions: %w", err)
	}
	defer rows.Close()
	var out []Action
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, "", fmt.Errorf("scan proposed action: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list proposed actions: %w", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		last := out[len(out)-1]
		if next, err = encodeCursor(listCursor{CreatedAt: last.CreatedAt, ID: last.ID}); err != nil {
			return nil, "", err
		}
	}
	return out, next, nil
}

// Get returns one of the caller's proposals, lazily marking an overdue
// pending row expired. Another identity's row yields NotFoundError{Mismatch}.
func (s *Store) Get(ctx context.Context, identityID, id string) (Action, error) {
	if !uuidPattern.MatchString(id) {
		return Action{}, &NotFoundError{}
	}
	a, err := scanAction(s.pool.QueryRow(ctx,
		`SELECT `+actionColumns+` FROM ai_proposed_action WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Action{}, &NotFoundError{}
	}
	if err != nil {
		return Action{}, fmt.Errorf("get proposed action: %w", err)
	}
	if a.IdentityID != identityID {
		return Action{}, &NotFoundError{Mismatch: true}
	}
	if a.Status == StatusPending && !a.ExpiresAt.After(time.Now()) {
		a, err = scanAction(s.pool.QueryRow(ctx, `
			UPDATE ai_proposed_action SET status = 'expired', resolved_at = now()
			WHERE id = $1 AND identity_id = $2 AND status = 'pending' AND expires_at <= now()
			RETURNING `+actionColumns, id, identityID))
		if errors.Is(err, pgx.ErrNoRows) {
			return s.Get(ctx, identityID, id) // resolved concurrently; re-read
		}
		if err != nil {
			return Action{}, fmt.Errorf("expire proposed action: %w", err)
		}
	}
	return a, nil
}
