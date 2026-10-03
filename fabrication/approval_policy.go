package fabrication

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
)

// ApprovalPolicy configures the ordered approvers copied onto future requests.
type ApprovalPolicy struct {
	SubjectKind string `json:"subjectKind"`
	EmployeeIDs []int  `json:"employeeIds"`
}

// SetApprovalPolicy changes future request configuration after a transactional permission check.
func SetApprovalPolicy(ctx context.Context, pool *pgxpool.Pool, actor ActionActor, in ApprovalPolicy) error {
	if in.SubjectKind != "template" && in.SubjectKind != "remake" {
		return ClientError{Msg: "Unknown fabrication approval type."}
	}
	if len(in.EmployeeIDs) == 0 {
		return ClientError{Msg: "Configure at least one approver."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin approval policy: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	decision, err := authz.Check(ctx, tx, actor.IdentityID, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if err != nil {
		return fmt.Errorf("authorize approval policy: %w", err)
	}
	if !decision.Allowed {
		return ErrActionPermission
	}
	seen := map[int]bool{}
	for _, id := range in.EmployeeIDs {
		if seen[id] {
			return ClientError{Msg: "Each approval stage must use a different approver."}
		}
		seen[id] = true
		var live bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM employee e JOIN users u ON u.id=e.employee_user_id WHERE e.employee_id=$1 AND e.employee_deleted_at IS NULL AND u.status='active')`, id).Scan(&live)
		if err != nil {
			return fmt.Errorf("validate approver: %w", err)
		}
		if !live {
			return approvalchain.ErrUnknownApprover
		}
	}
	_, err = tx.Exec(ctx, `INSERT INTO fabrication_approval_policy(subject_kind,employee_ids,updated_by) VALUES($1,$2,$3)
 ON CONFLICT(subject_kind) DO UPDATE SET employee_ids=EXCLUDED.employee_ids,updated_by=EXCLUDED.updated_by,updated_at=NOW()`, in.SubjectKind, in.EmployeeIDs, nullableInt(actor.EmployeeID))
	if err != nil {
		return fmt.Errorf("save approval policy: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit approval policy: %w", err)
	}
	return nil
}

func snapshotTemplateApprovers(ctx context.Context, tx pgx.Tx, templateID int64) error {
	var ids []int
	err := tx.QueryRow(ctx, `SELECT employee_ids FROM fabrication_approval_policy WHERE subject_kind='template' FOR SHARE`).Scan(&ids)
	if err == pgx.ErrNoRows {
		return ClientError{Msg: "Configure template approvers before submitting measurements."}
	}
	if err != nil {
		return fmt.Errorf("load template approval policy: %w", err)
	}
	steps := make([]approvalchain.SequenceStep, 0, len(ids))
	for _, id := range ids {
		steps = append(steps, approvalchain.SequenceStep{EmployeeID: id})
	}
	if len(steps) == 0 {
		return ClientError{Msg: "Configure at least one template approver."}
	}
	raw, err := json.Marshal(steps)
	if err != nil {
		return fmt.Errorf("encode approval snapshot: %w", err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO fabrication_revision_approval(template_id,steps) VALUES($1,$2)`, templateID, raw)
	if err != nil {
		return fmt.Errorf("snapshot template approvers: %w", err)
	}
	return nil
}
