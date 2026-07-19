// Package teamstore is the tenant-scoped store for teams and their membership.
// Teams live in the tenant database (teams / team_members); the connection pool
// is the tenant scope, so no query filters by tenant id.
package teamstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrTeamNotFound is returned when a team lookup misses.
// ErrUserNotFound is returned when adding a member whose user does not exist.
var (
	ErrTeamNotFound = errors.New("team not found")
	ErrUserNotFound = errors.New("user not found")
)

// Team is a workspace team with a member count for list views.
type Team struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

// Member is a user belonging to a team (denormalized for display).
type Member struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email"`
	FullName string `json:"full_name"`
}

// TeamDetail is a team plus its members.
type TeamDetail struct {
	Team
	Members []Member `json:"members"`
}

// ListTeams returns all teams with their member counts, newest first.
func ListTeams(ctx context.Context, pool *pgxpool.Pool) ([]Team, error) {
	rows, err := pool.Query(ctx, `
		SELECT t.id, t.name, t.created_at, COUNT(tm.user_id)
		FROM teams t
		LEFT JOIN team_members tm ON tm.team_id = t.id
		GROUP BY t.id, t.name, t.created_at
		ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()

	teams := make([]Team, 0)
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.MemberCount); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		teams = append(teams, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	return teams, nil
}

// GetTeam loads a single team with its members. Missing teams return
// ErrTeamNotFound.
func GetTeam(ctx context.Context, pool *pgxpool.Pool, id string) (*TeamDetail, error) {
	var d TeamDetail
	err := pool.QueryRow(ctx, `
		SELECT id, name, created_at FROM teams WHERE id = $1`, id).
		Scan(&d.ID, &d.Name, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTeamNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get team: %w", err)
	}

	rows, err := pool.Query(ctx, `
		SELECT u.id, u.email, u.full_name
		FROM team_members tm
		JOIN users u ON u.id = tm.user_id
		WHERE tm.team_id = $1
		ORDER BY u.full_name, u.email`, id)
	if err != nil {
		return nil, fmt.Errorf("get team members: %w", err)
	}
	defer rows.Close()

	d.Members = make([]Member, 0)
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.UserID, &m.Email, &m.FullName); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		d.Members = append(d.Members, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get team members: %w", err)
	}
	d.MemberCount = len(d.Members)
	return &d, nil
}

// CreateTeam inserts a new team.
func CreateTeam(ctx context.Context, pool *pgxpool.Pool, name string) (*Team, error) {
	var t Team
	err := pool.QueryRow(ctx, `
		INSERT INTO teams (name) VALUES ($1)
		RETURNING id, name, created_at`, name).
		Scan(&t.ID, &t.Name, &t.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create team: %w", err)
	}
	return &t, nil
}

// UpdateTeam renames a team. Missing teams return ErrTeamNotFound.
func UpdateTeam(ctx context.Context, pool *pgxpool.Pool, id, name string) (*Team, error) {
	var t Team
	err := pool.QueryRow(ctx, `
		UPDATE teams SET name = $2 WHERE id = $1
		RETURNING id, name, created_at`, id, name).
		Scan(&t.ID, &t.Name, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTeamNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update team: %w", err)
	}
	return &t, nil
}

// DeleteTeam removes a team; membership rows cascade. Missing teams return
// ErrTeamNotFound.
func DeleteTeam(ctx context.Context, pool *pgxpool.Pool, id string) error {
	tag, err := pool.Exec(ctx, `DELETE FROM teams WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTeamNotFound
	}
	return nil
}

// AddMember adds a user to a team (idempotent). Returns ErrTeamNotFound when the
// team does not exist and ErrUserNotFound when the user does not exist.
func AddMember(ctx context.Context, pool *pgxpool.Pool, teamID, userID string) error {
	if err := teamExists(ctx, pool, teamID); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO team_members (team_id, user_id) VALUES ($1, $2)
		ON CONFLICT (team_id, user_id) DO NOTHING`, teamID, userID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" { // foreign_key_violation → bad user_id
		return ErrUserNotFound
	}
	if err != nil {
		return fmt.Errorf("add team member: %w", err)
	}
	return nil
}

// RemoveMember removes a user from a team. Returns ErrTeamNotFound when the team
// does not exist; removing a non-member is a no-op.
func RemoveMember(ctx context.Context, pool *pgxpool.Pool, teamID, userID string) error {
	if err := teamExists(ctx, pool, teamID); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `
		DELETE FROM team_members WHERE team_id = $1 AND user_id = $2`, teamID, userID)
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	return nil
}

// teamExists returns ErrTeamNotFound when no team with id exists.
func teamExists(ctx context.Context, pool *pgxpool.Pool, id string) error {
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM teams WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("team exists: %w", err)
	}
	if !exists {
		return ErrTeamNotFound
	}
	return nil
}
