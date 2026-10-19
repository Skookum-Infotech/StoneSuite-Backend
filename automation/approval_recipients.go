package automation

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/approvalchain"
)

// roleSuperAdmin is the roles.key of the immutable system role that holds every permission.
const roleSuperAdmin = "super_admin"

// approvalRecipient is one internal user to remind. Fallback marks an
// owner/super-admin escalation sent because no live approver remains.
type approvalRecipient struct {
	UserID     string
	IdentityID string
	Email      string
	Name       string
	Fallback   bool
}

// approvalRecipients returns who should be reminded about rec: live approvers
// configured at the gate with no sign-off row for this record at the gate status.
// When the gate is orphaned (no live approver at all) it escalates to the record
// owner (live user only) plus every active super admin, deduped by user.
// It also escalates when live approvers exist but have all signed already (the
// others are deactivated), since nobody left can act.
// One or two queries per record is acceptable: stuck records are few. The
// candidate query has no date prefilter and scans every record at the gate
// (a correlated MAX over history per row), which is fine at current volumes.
func approvalRecipients(ctx context.Context, q Querier, spec approvalSpec, rec ApprovalRecord) ([]approvalRecipient, error) {
	if err := spec.ident(); err != nil {
		return nil, err
	}
	sql := strings.NewReplacer("{apr}", spec.Cfg.ApproverTable, "{apl}", spec.Cfg.ApprovalTable,
		"{id}", spec.Cfg.Record.IDColumn).Replace(`
SELECT u.id::text, u.identity_id::text, u.email, u.full_name,
       EXISTS (SELECT 1 FROM {apl} a WHERE a.{id} = $3 AND a.record_status_id = ea.record_status_id
                 AND a.approver_employee_id = ea.approver_employee_id)
FROM {apr} ea` + approvalchain.LiveApproverJoin + `
WHERE ea.is_active
  AND ea.record_type_id = (SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1)
  AND ea.record_status_id = (SELECT s.record_status_id FROM lkp_record_status s
        JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
        WHERE t.record_type_code = $1 AND s.record_status_code = $2)
ORDER BY u.email`)
	rows, err := q.Query(ctx, sql, spec.RecordTypeCode, spec.Gate.StatusCode, rec.InternalID)
	if err != nil {
		return nil, fmt.Errorf("%s approval recipients: %w", spec.Workflow, err)
	}
	defer rows.Close()
	var out []approvalRecipient
	for rows.Next() {
		var r approvalRecipient
		var signed bool
		if err := rows.Scan(&r.UserID, &r.IdentityID, &r.Email, &r.Name, &signed); err != nil {
			return nil, fmt.Errorf("scan %s approval recipient: %w", spec.Workflow, err)
		}
		if !signed {
			out = append(out, r)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s approval recipients: %w", spec.Workflow, err)
	}
	if len(out) > 0 {
		return out, nil
	}
	return orphanRecipients(ctx, q, rec)
}

// orphanRecipients is the escalation list for a gate with no live approver:
// the record owner (if an active user) plus every active super admin, one row per user.
func orphanRecipients(ctx context.Context, q Querier, rec ApprovalRecord) ([]approvalRecipient, error) {
	rows, err := q.Query(ctx, `
SELECT u.id::text, u.identity_id::text, u.email, u.full_name
FROM users u
WHERE u.status = $1
  AND (u.id = (SELECT e.employee_user_id FROM employee e
               WHERE e.employee_id = $2 AND e.employee_deleted_at IS NULL)
       OR EXISTS (SELECT 1 FROM user_roles ur JOIN roles r ON r.id = ur.role_id
                  WHERE ur.user_id = u.id AND r.key = $3))
ORDER BY u.email, u.id`, userStatusActive, rec.OwnerEmployeeID, roleSuperAdmin)
	if err != nil {
		return nil, fmt.Errorf("approval fallback recipients: %w", err)
	}
	defer rows.Close()
	var out []approvalRecipient
	for rows.Next() {
		r := approvalRecipient{Fallback: true}
		if err := rows.Scan(&r.UserID, &r.IdentityID, &r.Email, &r.Name); err != nil {
			return nil, fmt.Errorf("scan approval fallback recipient: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate approval fallback recipients: %w", err)
	}
	return out, nil
}
