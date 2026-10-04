package fabrication

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
)

// RevisionDecisionInput records a configured internal approver's decision.
type RevisionDecisionInput struct {
	CommandMeta
	Approve bool   `json:"approve"`
	Reason  string `json:"reason"`
}

// CustomerApprovalInput records externally received evidence against one revision.
type CustomerApprovalInput struct {
	CommandMeta
	Approver   string `json:"approver"`
	Channel    string `json:"channel"`
	Evidence   string `json:"evidence"`
	ApprovedAt string `json:"approvedAt"`
}

// DecideRevision records only the next configured approver's decision.
func DecideRevision(ctx context.Context, pool *pgxpool.Pool, jobID, revisionID string, actor ActionActor, in RevisionDecisionInput) (ActionResult, error) {
	actor.permission = authz.ActionTransition
	return decideRevision(ctx, pool, jobID, revisionID, actor, in)
}

func decideRevision(ctx context.Context, pool *pgxpool.Pool, jobID, revisionID string, actor ActionActor, in RevisionDecisionInput) (ActionResult, error) {
	payload, err := json.Marshal(in)
	if err != nil {
		return ActionResult{}, fmt.Errorf("encode revision decision: %w", err)
	}
	return executeAction(ctx, pool, jobID, actor, in.CommandMeta, actionCommand{Code: "decide_revision", SubjectID: revisionID, Payload: payload}, func(ctx context.Context, tx pgx.Tx, s actionState) error {
		templateID, err := lockCurrentRevision(ctx, tx, s.JobID, revisionID)
		if err != nil {
			return err
		}
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT steps FROM fabrication_revision_approval WHERE template_id=$1 FOR UPDATE`, templateID).Scan(&raw); err != nil {
			return fmt.Errorf("load revision approvers: %w", err)
		}
		var steps []approvalchain.SequenceStep
		if err = json.Unmarshal(raw, &steps); err != nil {
			return fmt.Errorf("decode revision approvers: %w", err)
		}
		index, done, err := approvalchain.NextSequenceDecision(steps, actor.EmployeeID)
		if err != nil {
			return ErrNotApprover
		}
		if !in.Approve {
			if strings.TrimSpace(in.Reason) == "" {
				return ClientError{Msg: "Explain why the template needs changes."}
			}
			if _, err = tx.Exec(ctx, `UPDATE fabrication_revision_approval SET rejected_at=NOW(),rejection_reason=$2 WHERE template_id=$1`, templateID, in.Reason); err != nil {
				return fmt.Errorf("record template rejection: %w", err)
			}
			_, err = tx.Exec(ctx, `UPDATE fabrication_template_revision SET state='rejected' WHERE template_id=$1`, templateID)
			if err != nil {
				return fmt.Errorf("reject template: %w", err)
			}
			return nil
		}
		steps[index].Approved = true
		raw, err = json.Marshal(steps)
		if err != nil {
			return fmt.Errorf("encode approval sequence: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE fabrication_revision_approval SET steps=$2,internal_approved_at=CASE WHEN $3 THEN NOW() ELSE internal_approved_at END WHERE template_id=$1`, templateID, raw, done); err != nil {
			return fmt.Errorf("record template decision: %w", err)
		}
		return finalizeRevision(ctx, tx, templateID)
	})
}

// RecordCustomerApproval stores customer evidence; it never substitutes for internal approval.
func RecordCustomerApproval(ctx context.Context, pool *pgxpool.Pool, jobID, revisionID string, actor ActionActor, in CustomerApprovalInput) (ActionResult, error) {
	actor.permission = authz.ActionTransition
	return recordCustomerApproval(ctx, pool, jobID, revisionID, actor, in)
}

func recordCustomerApproval(ctx context.Context, pool *pgxpool.Pool, jobID, revisionID string, actor ActionActor, in CustomerApprovalInput) (ActionResult, error) {
	at, err := time.Parse(time.RFC3339, in.ApprovedAt)
	if err != nil || at.After(time.Now()) || strings.TrimSpace(in.Approver) == "" || strings.TrimSpace(in.Evidence) == "" {
		return ActionResult{}, ClientError{Msg: "Provide the customer approver, evidence and a valid past approval date."}
	}
	switch in.Channel {
	case "email", "signed_document", "message", "portal":
	default:
		return ActionResult{}, ClientError{Msg: "Select a supported approval evidence channel."}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return ActionResult{}, fmt.Errorf("encode customer approval: %w", err)
	}
	return executeAction(ctx, pool, jobID, actor, in.CommandMeta, actionCommand{Code: "record_customer_approval", SubjectID: revisionID, Payload: payload}, func(ctx context.Context, tx pgx.Tx, s actionState) error {
		templateID, err := lockCurrentRevision(ctx, tx, s.JobID, revisionID)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE fabrication_revision_approval SET customer_approved_at=$2,customer_approver=$3,customer_channel=$4,customer_evidence=$5,customer_recorded_by=$6,customer_recorded_at=NOW() WHERE template_id=$1 AND customer_approved_at IS NULL`, templateID, at, in.Approver, in.Channel, in.Evidence, nullableInt(actor.EmployeeID))
		if err != nil {
			return fmt.Errorf("record customer evidence: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrActionConflict
		}
		return finalizeRevision(ctx, tx, templateID)
	})
}

func lockCurrentRevision(ctx context.Context, tx pgx.Tx, jobID int, revisionID string) (int64, error) {
	var id int64
	var state string
	var submitted, current int64
	err := tx.QueryRow(ctx, `SELECT t.template_id,t.state,t.sales_order_version,so.sales_order_record_version
 FROM fabrication_template_revision t JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id JOIN sales_order so ON so.sales_order_id=j.sales_order_id
 WHERE t.fabrication_job_id=$1 AND t.template_uuid=$2 AND so.sales_order_deleted_at IS NULL FOR UPDATE OF t,so`, jobID, revisionID).Scan(&id, &state, &submitted, &current)
	if err == pgx.ErrNoRows {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lock template revision: %w", err)
	}
	if state != "submitted" || submitted != current {
		return 0, ErrActionConflict
	}
	return id, nil
}

func finalizeRevision(ctx context.Context, tx pgx.Tx, id int64) error {
	_, err := tx.Exec(ctx, `UPDATE fabrication_template_revision t SET state='approved' FROM fabrication_revision_approval a
 WHERE t.template_id=$1 AND a.template_id=t.template_id AND a.internal_approved_at IS NOT NULL AND a.rejected_at IS NULL
 AND (NOT (t.change_summary->>'customerRequired')::boolean OR a.customer_approved_at IS NOT NULL)`, id)
	if err != nil {
		return fmt.Errorf("finalize template approval: %w", err)
	}
	return nil
}
