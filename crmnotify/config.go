package crmnotify

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ConfigurableGroups are the recipient groups a tenant maps to roles.
var ConfigurableGroups = []Group{GroupManager, GroupFinance}

// ErrInvalidRoles is returned when a saved role id is malformed or names no role.
var ErrInvalidRoles = errors.New("one or more role ids do not match an existing role")

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// RoleRef names a role mapped to a recipient group.
type RoleRef struct {
	ID   string `json:"roleId"`
	Name string `json:"roleName"`
}

// LoadRecipientRoles returns, per configurable group, the roles mapped to it.
// Every group is present in the result, empty when nothing is configured.
func LoadRecipientRoles(ctx context.Context, q workflow.Querier) (map[Group][]RoleRef, error) {
	out := map[Group][]RoleRef{}
	for _, g := range ConfigurableGroups {
		out[g] = []RoleRef{}
	}
	rows, err := q.Query(ctx, `
		SELECT cr.recipient_group, r.id::text, r.name
		FROM crm_notify_recipient_role cr
		JOIN roles r ON r.id = cr.role_id
		ORDER BY cr.recipient_group, r.name`)
	if err != nil {
		return nil, fmt.Errorf("load crm notify recipient roles: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		var ref RoleRef
		if err := rows.Scan(&g, &ref.ID, &ref.Name); err != nil {
			return nil, fmt.Errorf("scan crm notify recipient role: %w", err)
		}
		out[Group(g)] = append(out[Group(g)], ref)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate crm notify recipient roles: %w", err)
	}
	return out, nil
}

// SaveRecipientRoles replaces the role mapping of every configurable group in
// one transaction. A group missing from groups is cleared. Returns
// ErrInvalidRoles when any id is not a UUID of an existing role.
func SaveRecipientRoles(ctx context.Context, pool *pgxpool.Pool, groups map[Group][]string) error {
	for _, ids := range groups {
		for _, id := range ids {
			if !uuidPattern.MatchString(id) {
				return ErrInvalidRoles
			}
		}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin crm notify config tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM crm_notify_recipient_role`); err != nil {
		return fmt.Errorf("clear crm notify recipient roles: %w", err)
	}
	for _, g := range ConfigurableGroups {
		for _, id := range dedupe(groups[g]) {
			tag, err := tx.Exec(ctx, `
				INSERT INTO crm_notify_recipient_role (recipient_group, role_id)
				SELECT $1, id FROM roles WHERE id = $2::uuid
				ON CONFLICT DO NOTHING`, string(g), id)
			if err != nil {
				return fmt.Errorf("save crm notify recipient role: %w", err)
			}
			if tag.RowsAffected() == 0 {
				return ErrInvalidRoles
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit crm notify config: %w", err)
	}
	return nil
}

// dedupe drops repeated ids, keeping order, so a repeated id is not mistaken
// for an unknown role by the insert's row count.
func dedupe(ids []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
