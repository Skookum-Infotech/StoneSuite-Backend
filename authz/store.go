package authz

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/cache"
	"stonesuite-backend/middleware"
)

// Querier is the subset of pgx behavior the store needs. Defined here (consumer
// side) so a *pgxpool.Pool or a pgx.Tx can both be passed in.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ErrRoleNotFound is returned when a role lookup misses.
var ErrRoleNotFound = errors.New("role not found")

// Grant is a single permission line on a role.
type Grant struct {
	Resource Resource `json:"resource"`
	Action   Action   `json:"action"`
	Scope    Scope    `json:"scope"`
}

// Role is a tenant-defined bundle of grants.
type Role struct {
	ID          string  `json:"id"`
	Key         string  `json:"key"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	IsSystem    bool    `json:"isSystem"`
	Permissions []Grant `json:"permissions"`
}

// ----- effective permissions (for the enforcer) -----------------------------

// grantsCacheKey identifies cached effective grants by tenant pool + identity
// + active role (an empty activeRoleID is its own cache bucket, distinct from
// any specific role, so switching roles never serves stale grants).
type grantsCacheKey struct {
	pool         *pgxpool.Pool
	identityID   string
	activeRoleID string
}

// grantsCache holds recently-resolved effective grants (role_permissions
// rarely change but are checked on nearly every request). Entries are
// invalidated tenant-wide on any role/assignment write.
var grantsCache = cache.New[grantsCacheKey, []Grant](30 * time.Second)

// invalidateGrants drops all cached grants for q's tenant pool, if q is a
// tenant pool (transactions don't populate the cache, so nothing to do).
func invalidateGrants(q Querier) {
	if pool, ok := q.(*pgxpool.Pool); ok {
		grantsCache.DeleteFunc(func(k grantsCacheKey) bool { return k.pool == pool })
	}
}

// EffectiveGrants returns every grant the given control-plane identity has in
// this tenant, resolved through users -> user_roles -> role_permissions.
// Results are cached briefly per tenant pool (ADR-3); callers passing a
// *pgxpool.Pool benefit from the cache, callers inside a transaction (pgx.Tx)
// always read through.
//
// If ctx carries a request context with an active role selected (see
// POST /api/tenant/auth/switch-role), grants are narrowed to that one role
// instead of the caller's full assigned set — a hard context switch, not a
// UI filter. ctx without a request context (e.g. background jobs) behaves as
// before: all assigned roles apply.
func EffectiveGrants(ctx context.Context, q Querier, identityID string) ([]Grant, error) {
	activeRoleID := activeRoleFromContext(ctx)
	if pool, ok := q.(*pgxpool.Pool); ok {
		key := grantsCacheKey{pool: pool, identityID: identityID, activeRoleID: activeRoleID}
		if g, ok := grantsCache.Get(key); ok {
			return g, nil
		}
		g, err := loadEffectiveGrants(ctx, q, identityID, activeRoleID)
		if err != nil {
			return nil, err
		}
		grantsCache.Set(key, g)
		return g, nil
	}
	return loadEffectiveGrants(ctx, q, identityID, activeRoleID)
}

// activeRoleFromContext returns the caller's active role id from the request
// context, or "" if none is set (or ctx carries no request context at all).
func activeRoleFromContext(ctx context.Context) string {
	payload, err := middleware.GetUserFromContext(ctx)
	if err != nil {
		return ""
	}
	return payload.ActiveRoleID
}

func loadEffectiveGrants(ctx context.Context, q Querier, identityID, activeRoleID string) ([]Grant, error) {
	query := `
		SELECT rp.resource, rp.action, rp.scope
		FROM users u
		JOIN user_roles ur       ON ur.user_id = u.id
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		WHERE u.identity_id = $1`
	args := []any{identityID}
	if activeRoleID != "" {
		query += ` AND ur.role_id = $2`
		args = append(args, activeRoleID)
	}
	rows, err := q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("effective grants: %w", err)
	}
	defer rows.Close()

	var out []Grant
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Resource, &g.Action, &g.Scope); err != nil {
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// ----- role management (for the role editor API) ----------------------------

// ListRoles returns all roles with their permissions.
func ListRoles(ctx context.Context, q Querier) ([]Role, error) {
	rows, err := q.Query(ctx, `
		SELECT id, key, name, description, is_system
		FROM roles ORDER BY is_system DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()

	roles := []Role{}
	index := map[string]int{}
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Key, &r.Name, &r.Description, &r.IsSystem); err != nil {
			return nil, fmt.Errorf("scan role: %w", err)
		}
		r.Permissions = []Grant{}
		index[r.ID] = len(roles)
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	prows, err := q.Query(ctx, `SELECT role_id, resource, action, scope FROM role_permissions`)
	if err != nil {
		return nil, fmt.Errorf("list role permissions: %w", err)
	}
	defer prows.Close()
	for prows.Next() {
		var roleID string
		var g Grant
		if err := prows.Scan(&roleID, &g.Resource, &g.Action, &g.Scope); err != nil {
			return nil, fmt.Errorf("scan role permission: %w", err)
		}
		if i, ok := index[roleID]; ok {
			roles[i].Permissions = append(roles[i].Permissions, g)
		}
	}
	return roles, prows.Err()
}

// GetRole loads a single role with its permissions.
func GetRole(ctx context.Context, q Querier, id string) (*Role, error) {
	var r Role
	err := q.QueryRow(ctx,
		`SELECT id, key, name, description, is_system FROM roles WHERE id = $1`, id).
		Scan(&r.ID, &r.Key, &r.Name, &r.Description, &r.IsSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrRoleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get role: %w", err)
	}
	rows, err := q.Query(ctx,
		`SELECT resource, action, scope FROM role_permissions WHERE role_id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("get role permissions: %w", err)
	}
	defer rows.Close()
	r.Permissions = []Grant{}
	for rows.Next() {
		var g Grant
		if err := rows.Scan(&g.Resource, &g.Action, &g.Scope); err != nil {
			return nil, fmt.Errorf("scan grant: %w", err)
		}
		r.Permissions = append(r.Permissions, g)
	}
	return &r, rows.Err()
}

// CreateRole inserts a custom (non-system) role and its validated permissions.
// The INSERT and permission writes are wrapped in a transaction so a crash
// mid-insert never leaves a role with no permissions.
func CreateRole(ctx context.Context, q Querier, key, name, description string, perms []Grant) (string, error) {
	if err := validateGrants(perms); err != nil {
		return "", err
	}
	pool, ok := q.(*pgxpool.Pool)
	if !ok {
		// q is already a transaction — just write directly.
		return createRoleInTx(ctx, q, key, name, description, perms)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("create role: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	id, err := createRoleInTx(ctx, tx, key, name, description, perms)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("create role: commit: %w", err)
	}
	invalidateGrants(q)
	return id, nil
}

func createRoleInTx(ctx context.Context, q Querier, key, name, description string, perms []Grant) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO roles (key, name, description, is_system)
		VALUES ($1, $2, $3, FALSE) RETURNING id`, key, name, description).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("create role: %w", err)
	}
	if err := replacePermissions(ctx, q, id, perms); err != nil {
		return "", err
	}
	return id, nil
}

// UpdateRole updates a role's name/description and replaces its permissions.
// System roles' permissions are immutable (their key/name may be edited only
// via migrations); callers must guard with IsSystem before calling for those.
// The UPDATE and permission replacement are wrapped in a transaction so a crash
// mid-update never leaves the role with a partial permission set.
func UpdateRole(ctx context.Context, q Querier, id, name, description string, perms []Grant) error {
	if err := validateGrants(perms); err != nil {
		return err
	}
	pool, ok := q.(*pgxpool.Pool)
	if !ok {
		// q is already a transaction — write directly.
		return updateRoleInTx(ctx, q, id, name, description, perms)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("update role: begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if err := updateRoleInTx(ctx, tx, id, name, description, perms); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("update role: commit: %w", err)
	}
	invalidateGrants(q)
	return nil
}

func updateRoleInTx(ctx context.Context, q Querier, id, name, description string, perms []Grant) error {
	tag, err := q.Exec(ctx,
		`UPDATE roles SET name = $2, description = $3, updated_at = NOW() WHERE id = $1`,
		id, name, description)
	if err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRoleNotFound
	}
	if err := replacePermissions(ctx, q, id, perms); err != nil {
		return err
	}
	return nil
}

// DeleteRole removes a custom role. System roles cannot be deleted.
func DeleteRole(ctx context.Context, q Querier, id string) error {
	tag, err := q.Exec(ctx, `DELETE FROM roles WHERE id = $1 AND is_system = FALSE`, id)
	if err != nil {
		return fmt.Errorf("delete role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRoleNotFound // either absent or a protected system role
	}
	invalidateGrants(q)
	return nil
}

// replacePermissions swaps a role's permission set atomically-ish (delete+insert).
func replacePermissions(ctx context.Context, q Querier, roleID string, perms []Grant) error {
	if _, err := q.Exec(ctx, `DELETE FROM role_permissions WHERE role_id = $1`, roleID); err != nil {
		return fmt.Errorf("clear role permissions: %w", err)
	}
	for _, g := range perms {
		if _, err := q.Exec(ctx, `
			INSERT INTO role_permissions (role_id, resource, action, scope)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (role_id, resource, action) DO UPDATE SET scope = EXCLUDED.scope`,
			roleID, g.Resource, g.Action, g.Scope); err != nil {
			return fmt.Errorf("insert role permission: %w", err)
		}
	}
	return nil
}

func validateGrants(perms []Grant) error {
	for _, g := range perms {
		if !IsValidPermission(g.Resource, g.Action) {
			return fmt.Errorf("unknown permission %s:%s", g.Resource, g.Action)
		}
		if !IsValidScope(g.Scope) {
			return fmt.Errorf("invalid scope %q for %s:%s", g.Scope, g.Resource, g.Action)
		}
	}
	return nil
}

// ----- user <-> role assignment ---------------------------------------------

// AssignRole grants a role to a tenant user (idempotent).
func AssignRole(ctx context.Context, q Querier, userID, roleID string) error {
	_, err := q.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id) VALUES ($1, $2)
		ON CONFLICT (user_id, role_id) DO NOTHING`, userID, roleID)
	if err != nil {
		return fmt.Errorf("assign role: %w", err)
	}
	invalidateGrants(q)
	return nil
}

// UnassignRole removes a role from a user.
func UnassignRole(ctx context.Context, q Querier, userID, roleID string) error {
	if _, err := q.Exec(ctx,
		`DELETE FROM user_roles WHERE user_id = $1 AND role_id = $2`, userID, roleID); err != nil {
		return fmt.Errorf("unassign role: %w", err)
	}
	invalidateGrants(q)
	return nil
}

// RolesForUser lists the role ids assigned to a tenant user.
func RolesForUser(ctx context.Context, q Querier, userID string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT role_id FROM user_roles WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("roles for user: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan role id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
