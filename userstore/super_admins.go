package userstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// AdminContact is who to email about a workspace-level alert.
type AdminContact struct {
	IdentityID string
	Email      string
	FullName   string
}

// ListActiveSuperAdmins returns the workspace's users holding the super_admin
// role who are not disabled, for alerts addressed to the customer's admins.
func ListActiveSuperAdmins(ctx context.Context, q Querier) ([]AdminContact, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT u.identity_id::text, u.email, u.full_name
		FROM users u
		JOIN user_roles ur ON ur.user_id = u.id
		JOIN roles r ON r.id = ur.role_id
		WHERE r.key = 'super_admin' AND u.status = 'active'
		ORDER BY u.email`)
	if err != nil {
		return nil, fmt.Errorf("list super admins: %w", err)
	}
	admins, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AdminContact, error) {
		var a AdminContact
		err := row.Scan(&a.IdentityID, &a.Email, &a.FullName)
		return a, err
	})
	if err != nil {
		return nil, fmt.Errorf("list super admins: scan: %w", err)
	}
	return admins, nil
}
