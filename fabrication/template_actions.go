package fabrication

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
)

// TemplateActions describes decisions available to the current employee on a revision.
func TemplateActions(ctx context.Context, pool *pgxpool.Pool, revision TemplateRevision, identity string, employee int) ([]AvailableAction, error) {
	out := []AvailableAction{}
	if revision.State != "submitted" {
		return out, nil
	}
	decision, err := authz.Check(ctx, pool, identity, authz.ResourceInstallation, authz.ActionTransition)
	if err != nil {
		return nil, fmt.Errorf("authorize template decisions: %w", err)
	}
	if !decision.Allowed {
		return out, nil
	}
	var stepsRaw []byte
	var internal, customer bool
	err = pool.QueryRow(ctx, `SELECT a.steps,a.internal_approved_at IS NOT NULL,a.customer_approved_at IS NOT NULL
 FROM fabrication_revision_approval a JOIN fabrication_template_revision t ON t.template_id=a.template_id WHERE t.template_uuid=$1`, revision.ID).Scan(&stepsRaw, &internal, &customer)
	if err != nil {
		return nil, fmt.Errorf("read template decision state: %w", err)
	}
	var steps []approvalchain.SequenceStep
	if err = json.Unmarshal(stepsRaw, &steps); err != nil {
		return nil, fmt.Errorf("decode template sequence: %w", err)
	}
	if _, _, err = approvalchain.NextSequenceDecision(steps, employee); err == nil {
		out = append(out, AvailableAction{Code: "approve_revision", Label: "Approve template", Enabled: true, InputType: "revision_decision", Blockers: []ActionBlocker{}}, AvailableAction{Code: "reject_revision", Label: "Request changes", Enabled: true, InputType: "revision_decision", Blockers: []ActionBlocker{}})
	}
	if revision.Change.CustomerRequired && !customer {
		blockers := []ActionBlocker{}
		if !internal {
			blockers = append(blockers, ActionBlocker{Code: "internal_approval_pending", Message: "Complete internal review before requesting customer approval."})
		}
		out = append(out, AvailableAction{Code: "record_customer_approval", Label: "Record customer approval", Enabled: true, InputType: "customer_evidence", Blockers: []ActionBlocker{}}, AvailableAction{Code: "share_customer_review", Label: "Open customer review", Enabled: internal, InputType: "customer_review", Blockers: blockers})
	}
	return out, nil
}
