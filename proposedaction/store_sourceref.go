package proposedaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// maxSourceRefRows caps ListBySourceRef so one prefix can't return unbounded rows.
const maxSourceRefRows = 200

// likeEscaper escapes LIKE metacharacters so a prefix matches literally.
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// escapeLikePrefix escapes %, _ and \ so s is matched literally by LIKE ... ESCAPE '\'.
func escapeLikePrefix(s string) string { return likeEscaper.Replace(s) }

// reportExpired marks a pending row past its expiry as expired in the returned
// value only; nothing is written.
func reportExpired(a Action, now time.Time) Action {
	if a.Status == StatusPending && !a.ExpiresAt.After(now) {
		a.Status = StatusExpired
	}
	return a
}

// ListBySourceRef returns the caller's own rows from source whose payload
// sourceRef starts with refPrefix, newest first. Overdue pending rows are
// reported as expired without being mutated.
func (s *Store) ListBySourceRef(ctx context.Context, identityID string, source Source, refPrefix string) ([]Action, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+actionColumns+` FROM ai_proposed_action
		WHERE identity_id = $1 AND source = $2 AND payload->>'sourceRef' LIKE $3 ESCAPE '\'
		ORDER BY created_at DESC, id DESC LIMIT $4`,
		identityID, string(source), escapeLikePrefix(refPrefix)+"%", maxSourceRefRows)
	if err != nil {
		return nil, fmt.Errorf("list proposed actions by source ref: %w", err)
	}
	defer rows.Close()
	now := time.Now()
	var out []Action
	for rows.Next() {
		a, err := scanAction(rows)
		if err != nil {
			return nil, fmt.Errorf("scan proposed action: %w", err)
		}
		out = append(out, reportExpired(a, now))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list proposed actions by source ref: %w", err)
	}
	return out, nil
}

// FindActiveBySourceRef returns the caller's newest row from source with the
// exact sourceRef that is confirmed, or pending and unexpired.
func (s *Store) FindActiveBySourceRef(ctx context.Context, identityID string, source Source, ref string) (Action, bool, error) {
	a, err := scanAction(s.pool.QueryRow(ctx, `
		SELECT `+actionColumns+` FROM ai_proposed_action
		WHERE identity_id = $1 AND source = $2 AND payload->>'sourceRef' = $3
		  AND (status = 'confirmed' OR (status = 'pending' AND expires_at > now()))
		ORDER BY created_at DESC, id DESC LIMIT 1`,
		identityID, string(source), ref))
	if errors.Is(err, pgx.ErrNoRows) {
		return Action{}, false, nil
	}
	if err != nil {
		return Action{}, false, fmt.Errorf("find active proposed action by source ref: %w", err)
	}
	return a, true, nil
}
