package crmnotify

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/services"
	"stonesuite-backend/workflow"
)

// Recipient-group values stored in crm_notify_recipient_role.recipient_group.
const (
	roleGroupManager = string(GroupManager)
	roleGroupFinance = string(GroupFinance)
)

// contactCols selects a user's notifiable identity: users.identity_id is the id
// stonesuite-notify scopes its bell/feed by (NOT the tenant-local users.id).
const contactCols = `u.identity_id::text, u.email, u.full_name`

// resolve turns the groups an event is addressed to into a de-duplicated
// recipient list for one record. A group that resolves to nobody (no owner, no
// role configured) is skipped; a query error fails the whole event so it is
// logged rather than half-sent.
func resolve(ctx context.Context, q workflow.Querier, groups []Group, rec *workflow.Record) ([]services.RecipientTarget, error) {
	seen := map[string]bool{}
	var out []services.RecipientTarget
	for _, g := range groups {
		targets, err := resolveGroup(ctx, q, g, rec)
		if err != nil {
			return nil, err
		}
		for _, t := range targets {
			if t.UserID == "" || seen[t.UserID] {
				continue
			}
			seen[t.UserID] = true
			out = append(out, t)
		}
	}
	return out, nil
}

func resolveGroup(ctx context.Context, q workflow.Querier, g Group, rec *workflow.Record) ([]services.RecipientTarget, error) {
	switch g {
	case GroupOwner:
		return ownerTargets(ctx, q, rec.OwnerUserID)
	case GroupManager:
		return roleGroupTargets(ctx, q, roleGroupManager)
	case GroupFinance:
		return roleGroupTargets(ctx, q, roleGroupFinance)
	case GroupApprovers:
		return approverTargets(ctx, q, rec.ID)
	case GroupSubmitter:
		return submitterTargets(ctx, q, rec)
	default:
		return nil, fmt.Errorf("unknown recipient group %q", g)
	}
}

// ownerTargets resolves the record owner. ownerUserID is the tenant-local
// users.id (workflow.Record.OwnerUserID); empty means the record has no owner.
func ownerTargets(ctx context.Context, q workflow.Querier, ownerUserID string) ([]services.RecipientTarget, error) {
	if ownerUserID == "" {
		return nil, nil
	}
	rows, err := q.Query(ctx, `SELECT `+contactCols+` FROM users u WHERE u.id = $1::uuid AND u.status = 'active'`, ownerUserID)
	if err != nil {
		return nil, fmt.Errorf("load record owner: %w", err)
	}
	return scanTargets(rows)
}

// roleGroupTargets resolves every active user holding a role the tenant mapped
// to group (manager or finance) in crm_notify_recipient_role.
func roleGroupTargets(ctx context.Context, q workflow.Querier, group string) ([]services.RecipientTarget, error) {
	rows, err := q.Query(ctx, `
		SELECT DISTINCT `+contactCols+`
		FROM crm_notify_recipient_role r
		JOIN user_roles ur ON ur.role_id = r.role_id
		JOIN users u ON u.id = ur.user_id
		WHERE r.recipient_group = $1 AND u.status = 'active'`, group)
	if err != nil {
		return nil, fmt.Errorf("load %s recipients: %w", group, err)
	}
	return scanTargets(rows)
}

// approverTargets resolves the customer stage's active approvers (the
// stage-scoped rows, crm_status_id IS NULL — see crmstore/relational_approval.go).
func approverTargets(ctx context.Context, q workflow.Querier, recordUUID string) ([]services.RecipientTarget, error) {
	rows, err := q.Query(ctx, `
		SELECT `+contactCols+`
		FROM crm_workflow_approver a
		JOIN customer c ON c.record_type = a.record_type_id
		JOIN employee e ON e.employee_id = a.approver_employee_id
		JOIN users u ON u.id = e.employee_user_id
		WHERE c.customer_uuid = $1::uuid AND a.is_active AND a.crm_status_id IS NULL AND u.status = 'active'`, recordUUID)
	if err != nil {
		return nil, fmt.Errorf("load approvers: %w", err)
	}
	return scanTargets(rows)
}

// submitterTargets resolves whoever created the record; a record created
// without a recorded creator falls back to its owner.
func submitterTargets(ctx context.Context, q workflow.Querier, rec *workflow.Record) ([]services.RecipientTarget, error) {
	rows, err := q.Query(ctx, `
		SELECT `+contactCols+`
		FROM customer c
		JOIN employee e ON e.employee_id = c.customer_created_by
		JOIN users u ON u.id = e.employee_user_id
		WHERE c.customer_uuid = $1::uuid AND u.status = 'active'`, rec.ID)
	if err != nil {
		return nil, fmt.Errorf("load submitter: %w", err)
	}
	targets, err := scanTargets(rows)
	if err != nil || len(targets) > 0 {
		return targets, err
	}
	return ownerTargets(ctx, q, rec.OwnerUserID)
}

func scanTargets(rows pgx.Rows) ([]services.RecipientTarget, error) {
	defer rows.Close()
	var out []services.RecipientTarget
	for rows.Next() {
		var t services.RecipientTarget
		if err := rows.Scan(&t.UserID, &t.Email, &t.Name); err != nil {
			return nil, fmt.Errorf("scan recipient: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recipients: %w", err)
	}
	return out, nil
}
