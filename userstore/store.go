// Package userstore provides tenant-DB operations for workspace user management.
// All functions query the per-tenant database; the caller supplies either a
// *pgxpool.Pool (for handlers) or a pgx.Tx (for atomic flows).
package userstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the minimal pgx surface this package needs.
// *pgxpool.Pool and pgx.Tx both satisfy it.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ErrUserNotFound is returned when a user lookup finds no matching row.
var ErrUserNotFound = errors.New("user not found")

// ErrDuplicateUser is returned when a user with that email/identity already exists.
var ErrDuplicateUser = errors.New("user already exists in this workspace")

// User is a workspace member profile in the tenant database.
type User struct {
	ID         string    `json:"id"`
	IdentityID string    `json:"identityId"`
	Email      string    `json:"email"`
	FullName   string    `json:"fullName"`
	Status     string    `json:"status"` // active | invited | disabled
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Roles      []RoleSummary `json:"roles,omitempty"`
}

// RoleSummary is a compact role view embedded in User responses.
type RoleSummary struct {
	ID   string `json:"id"`
	Key  string `json:"key"`
	Name string `json:"name"`
}

const userCols = `u.id, u.identity_id, u.email, u.full_name, u.status, u.created_at, u.updated_at`

// ListUsers returns all workspace users ordered by creation time, each annotated
// with their assigned roles.
func ListUsers(ctx context.Context, q Querier) ([]User, error) {
	rows, err := q.Query(ctx, `SELECT `+userCols+` FROM users u ORDER BY u.created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	index := map[string]int{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.IdentityID, &u.Email, &u.FullName,
			&u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		u.Roles = []RoleSummary{}
		index[u.ID] = len(users)
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return []User{}, nil
	}

	// Attach roles in a single query.
	rrows, err := q.Query(ctx, `
		SELECT ur.user_id, r.id, r.key, r.name
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id`)
	if err != nil {
		return nil, fmt.Errorf("list user roles: %w", err)
	}
	defer rrows.Close()
	for rrows.Next() {
		var userID string
		var rs RoleSummary
		if err := rrows.Scan(&userID, &rs.ID, &rs.Key, &rs.Name); err != nil {
			return nil, fmt.Errorf("scan user role: %w", err)
		}
		if i, ok := index[userID]; ok {
			users[i].Roles = append(users[i].Roles, rs)
		}
	}
	return users, rrows.Err()
}

// GetUserByID loads a single user with roles by their tenant-local user ID.
func GetUserByID(ctx context.Context, q Querier, id string) (*User, error) {
	var u User
	err := q.QueryRow(ctx,
		`SELECT `+userCols+` FROM users u WHERE u.id = $1`, id).
		Scan(&u.ID, &u.IdentityID, &u.Email, &u.FullName, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by id: %w", err)
	}
	if err := attachRoles(ctx, q, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUserByIdentityID loads a user by their control-plane identity ID.
func GetUserByIdentityID(ctx context.Context, q Querier, identityID string) (*User, error) {
	var u User
	err := q.QueryRow(ctx,
		`SELECT `+userCols+` FROM users u WHERE u.identity_id = $1`, identityID).
		Scan(&u.ID, &u.IdentityID, &u.Email, &u.FullName, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by identity: %w", err)
	}
	if err := attachRoles(ctx, q, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetUserByEmail loads a user by their email (case-insensitive).
func GetUserByEmail(ctx context.Context, q Querier, email string) (*User, error) {
	var u User
	err := q.QueryRow(ctx,
		`SELECT `+userCols+` FROM users u WHERE LOWER(u.email) = LOWER($1)`, email).
		Scan(&u.ID, &u.IdentityID, &u.Email, &u.FullName, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user by email: %w", err)
	}
	if err := attachRoles(ctx, q, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a new workspace member. Returns ErrDuplicateUser if the
// identity or email is already registered in this tenant.
func CreateUser(ctx context.Context, q Querier, identityID, email, fullName, status string) (*User, error) {
	var u User
	err := q.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id, identity_id, email, full_name, status, created_at, updated_at`,
		identityID, email, fullName, status,
	).Scan(&u.ID, &u.IdentityID, &u.Email, &u.FullName, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if isDuplicateKeyErr(err) {
			return nil, ErrDuplicateUser
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	u.Roles = []RoleSummary{}
	return &u, nil
}

// UpdateUser updates a user's display name and/or status. Pass empty string to
// leave a field unchanged. Returns ErrUserNotFound if the ID does not exist.
func UpdateUser(ctx context.Context, q Querier, id, fullName, status string) (*User, error) {
	fullName = strings.TrimSpace(fullName)
	status = strings.TrimSpace(status)

	if fullName == "" && status == "" {
		return GetUserByID(ctx, q, id)
	}

	// Build a dynamic SET clause for only the supplied fields.
	setClauses := []string{"updated_at = NOW()"}
	args := []any{id}
	if fullName != "" {
		args = append(args, fullName)
		setClauses = append(setClauses, fmt.Sprintf("full_name = $%d", len(args)))
	}
	if status != "" {
		args = append(args, status)
		setClauses = append(setClauses, fmt.Sprintf("status = $%d", len(args)))
	}

	sql := fmt.Sprintf(
		`UPDATE users SET %s WHERE id = $1
		 RETURNING id, identity_id, email, full_name, status, created_at, updated_at`,
		strings.Join(setClauses, ", "),
	)

	var u User
	err := q.QueryRow(ctx, sql, args...).
		Scan(&u.ID, &u.IdentityID, &u.Email, &u.FullName, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUserNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("update user: %w", err)
	}
	if err := attachRoles(ctx, q, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// DeactivateUser sets a user's status to 'disabled' (soft delete).
// All role assignments are preserved for audit; access is blocked at login.
func DeactivateUser(ctx context.Context, q Querier, id string) error {
	tag, err := q.Exec(ctx,
		`UPDATE users SET status = 'disabled', updated_at = NOW() WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("deactivate user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// CountActiveSuperAdmins counts how many active users hold the super_admin role.
// Used to prevent removing the last super admin.
func CountActiveSuperAdmins(ctx context.Context, q Querier) (int, error) {
	var count int
	err := q.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM users u
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r ON r.id = ur.role_id
		WHERE r.key = 'super_admin' AND u.status != 'disabled'`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count active super admins: %w", err)
	}
	return count, nil
}

// IsSuperAdmin reports whether a user currently holds the super_admin role.
func IsSuperAdmin(ctx context.Context, q Querier, userID string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM user_roles ur
			JOIN roles r ON r.id = ur.role_id
			WHERE ur.user_id = $1 AND r.key = 'super_admin'
		)`, userID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("is super admin: %w", err)
	}
	return exists, nil
}

// UserIDByIdentityID returns the tenant-local user ID for a given identity ID.
// Returns ErrUserNotFound if no matching row exists.
func UserIDByIdentityID(ctx context.Context, q Querier, identityID string) (string, error) {
	var id string
	err := q.QueryRow(ctx, `SELECT id FROM users WHERE identity_id = $1`, identityID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrUserNotFound
	}
	if err != nil {
		return "", fmt.Errorf("user id by identity: %w", err)
	}
	return id, nil
}

// attachRoles loads the role summaries for u and sets u.Roles.
func attachRoles(ctx context.Context, q Querier, u *User) error {
	rows, err := q.Query(ctx, `
		SELECT r.id, r.key, r.name
		FROM user_roles ur
		JOIN roles r ON r.id = ur.role_id
		WHERE ur.user_id = $1`, u.ID)
	if err != nil {
		return fmt.Errorf("attach roles: %w", err)
	}
	defer rows.Close()
	u.Roles = []RoleSummary{}
	for rows.Next() {
		var rs RoleSummary
		if err := rows.Scan(&rs.ID, &rs.Key, &rs.Name); err != nil {
			return fmt.Errorf("scan role summary: %w", err)
		}
		u.Roles = append(u.Roles, rs)
	}
	return rows.Err()
}

// isDuplicateKeyErr reports whether the error is a PostgreSQL unique-constraint violation.
func isDuplicateKeyErr(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" // unique_violation
	}
	return false
}
