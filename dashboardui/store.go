package dashboardui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"stonesuite-backend/authz"
)

// Querier is the subset of pgx behavior the store needs. Defined here
// (consumer side) so a *pgxpool.Pool or a pgx.Tx can both be passed in.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Beginner is a Querier that can also start a transaction (e.g. *pgxpool.Pool).
type Beginner interface {
	Querier
	Begin(ctx context.Context) (pgx.Tx, error)
}

// RoleAllocation is one role's set of allocated dashboard widget ids.
type RoleAllocation struct {
	RoleID    string   `json:"roleId"`
	WidgetIDs []string `json:"widgetIds"`
}

// ErrRoleNotFound is returned when a roleId in a request does not match an
// existing role in this tenant.
var ErrRoleNotFound = authz.ErrRoleNotFound

// ErrRoleLocked is returned when a write targets a role holding the
// wildcard (super admin) grant -- that role always has every widget and
// cannot be individually configured.
var ErrRoleLocked = errors.New("role has a wildcard grant and cannot be configured")

// ErrInvalidWidgetID is returned when an allocation references a widget id
// outside the catalog whitelist.
type ErrInvalidWidgetID struct {
	WidgetID string
}

func (e *ErrInvalidWidgetID) Error() string {
	return fmt.Sprintf("unknown widget id %q", e.WidgetID)
}

// GetForRoles returns each given role's widget allocation, falling back to
// the catalog defaults for any role with no saved configuration -- a pure
// read, so an unconfigured role's defaults are computed on the fly rather
// than persisted as a side effect of GET.
func GetForRoles(ctx context.Context, q Querier, roleIDs []string) ([]RoleAllocation, error) {
	out := make([]RoleAllocation, 0, len(roleIDs))
	for _, roleID := range roleIDs {
		ids, err := getWidgetIDs(ctx, q, roleID)
		if err != nil {
			return nil, err
		}
		out = append(out, RoleAllocation{RoleID: roleID, WidgetIDs: ids})
	}
	return out, nil
}

func getWidgetIDs(ctx context.Context, q Querier, roleID string) ([]string, error) {
	var raw []byte
	err := q.QueryRow(ctx, `SELECT widget_ids FROM role_dashboard_widgets WHERE role_id = $1`, roleID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultWidgetIDs(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("get dashboard widgets for role %s: %w", roleID, err)
	}
	var ids []string
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &ids); err != nil {
			return nil, fmt.Errorf("decode widget ids for role %s: %w", roleID, err)
		}
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

// GetForIdentity resolves the dashboard widgets visible to one caller: every
// widget allocated across their assigned role(s), narrowed to just the
// active role when one is set (mirrors authz.EffectiveGrants' activeRoleId
// narrowing), unioned -- or the full catalog if any of their grants is the
// wildcard ('*','*') super-admin grant.
func GetForIdentity(ctx context.Context, q Querier, identityID, userID, activeRoleID string) ([]string, error) {
	grants, err := authz.EffectiveGrants(ctx, q, identityID)
	if err != nil {
		return nil, fmt.Errorf("load effective grants: %w", err)
	}
	if isWildcard(grants) {
		return WidgetIDs(), nil
	}

	roleIDs, err := authz.RolesForUser(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("load assigned roles: %w", err)
	}
	if activeRoleID != "" {
		roleIDs = filterToRole(roleIDs, activeRoleID)
	}

	allocations, err := GetForRoles(ctx, q, roleIDs)
	if err != nil {
		return nil, err
	}
	union := map[string]bool{}
	for _, a := range allocations {
		for _, id := range a.WidgetIDs {
			union[id] = true
		}
	}
	out := make([]string, 0, len(union))
	for id := range union {
		out = append(out, id)
	}
	return out, nil
}

// filterToRole narrows roleIDs to just activeRoleID, if present -- an active
// role that isn't actually one of the caller's assigned roles yields no
// roles rather than trusting the caller-supplied id, matching this
// codebase's fail-closed scope handling.
func filterToRole(roleIDs []string, activeRoleID string) []string {
	for _, id := range roleIDs {
		if id == activeRoleID {
			return []string{activeRoleID}
		}
	}
	return nil
}

// SetForRoles validates and atomically writes every given role's widget
// allocation in a single transaction -- a partial failure leaves every
// role's saved allocation untouched.
func SetForRoles(ctx context.Context, pool Beginner, allocations []RoleAllocation) error {
	for _, a := range allocations {
		for _, id := range a.WidgetIDs {
			if !IsValidWidgetID(id) {
				return &ErrInvalidWidgetID{WidgetID: id}
			}
		}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin set dashboard widgets: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, a := range allocations {
		role, err := authz.GetRole(ctx, tx, a.RoleID)
		if errors.Is(err, authz.ErrRoleNotFound) {
			return ErrRoleNotFound
		}
		if err != nil {
			return fmt.Errorf("load role %s: %w", a.RoleID, err)
		}
		if isWildcard(role.Permissions) {
			return ErrRoleLocked
		}

		raw, err := json.Marshal(a.WidgetIDs)
		if err != nil {
			return fmt.Errorf("encode widget ids for role %s: %w", a.RoleID, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO role_dashboard_widgets (role_id, widget_ids)
			VALUES ($1, $2::jsonb)
			ON CONFLICT (role_id) DO UPDATE
				SET widget_ids = EXCLUDED.widget_ids, updated_at = NOW()`,
			a.RoleID, raw); err != nil {
			return fmt.Errorf("upsert dashboard widgets for role %s: %w", a.RoleID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit set dashboard widgets: %w", err)
	}
	return nil
}

// isWildcard reports whether grants contains the '*','*' wildcard grant used
// by the seeded super_admin role (or any role holding it).
func isWildcard(grants []authz.Grant) bool {
	for _, g := range grants {
		if g.Resource == authz.ResourceAny && g.Action == authz.ActionAny {
			return true
		}
	}
	return false
}
