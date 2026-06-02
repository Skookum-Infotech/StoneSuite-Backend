package authz

import (
	"context"
	"fmt"
)

// RoleSuperAdmin is the seeded, undeletable system role granted full access.
const RoleSuperAdmin = "super_admin"

// SeedSuperAdmin ensures the super_admin system role exists with a single
// wildcard permission ('*','*','all') and returns its id. Idempotent: safe to
// call on every provision/migration. The Go enforcer expands the wildcard, so
// adding catalog entries never requires re-seeding existing tenants.
func SeedSuperAdmin(ctx context.Context, q Querier) (string, error) {
	var id string
	err := q.QueryRow(ctx, `
		INSERT INTO roles (key, name, description, is_system)
		VALUES ($1, 'Super Admin', 'Full access to this workspace.', TRUE)
		ON CONFLICT (LOWER(key)) DO UPDATE SET updated_at = NOW()
		RETURNING id`, RoleSuperAdmin).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("seed super_admin role: %w", err)
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO role_permissions (role_id, resource, action, scope)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (role_id, resource, action) DO UPDATE SET scope = EXCLUDED.scope`,
		id, ResourceAny, ActionAny, ScopeAll); err != nil {
		return "", fmt.Errorf("seed super_admin permission: %w", err)
	}
	return id, nil
}

// SeedTenantRBAC seeds the system roles for a freshly provisioned tenant and
// grants super_admin to its first user (the accepting identity). Idempotent.
func SeedTenantRBAC(ctx context.Context, q Querier, firstUserID string) error {
	roleID, err := SeedSuperAdmin(ctx, q)
	if err != nil {
		return err
	}
	if firstUserID == "" {
		return nil
	}
	return AssignRole(ctx, q, firstUserID, roleID)
}
