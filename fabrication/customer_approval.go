package fabrication

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/portal"
)

// ApproveTemplateAsCustomer records a digital decision from an authenticated
// portal identity. It re-resolves that identity's customer in the transaction.
func ApproveTemplateAsCustomer(ctx context.Context, pool *pgxpool.Pool, jobID, revisionID, identity string, meta CommandMeta) (ActionResult, error) {
	payload, err := json.Marshal(meta)
	if err != nil {
		return ActionResult{}, fmt.Errorf("encode portal decision: %w", err)
	}
	actor := ActionActor{IdentityID: identity, portalCustomer: true}
	return executeAction(ctx, pool, jobID, actor, meta, actionCommand{Code: "customer_approve_template", SubjectID: revisionID, Payload: payload}, func(ctx context.Context, tx pgx.Tx, state actionState) error {
		templateID, err := lockCurrentRevision(ctx, tx, state.JobID, revisionID)
		if err != nil {
			return err
		}
		session, err := portal.ResolveSession(ctx, tx, identity)
		if err != nil {
			return ErrNotFound
		}
		name := session.FullName
		if name == "" {
			name = session.Email
		}
		tag, err := tx.Exec(ctx, `UPDATE fabrication_revision_approval SET customer_approved_at=NOW(),customer_approver=$2,customer_channel='portal',customer_evidence=$3,customer_recorded_at=NOW()
  WHERE template_id=$1 AND internal_approved_at IS NOT NULL AND customer_approved_at IS NULL AND rejected_at IS NULL`, templateID, name, "Authenticated customer approval: "+identity)
		if err != nil {
			return fmt.Errorf("record portal decision: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrActionConflict
		}
		return finalizeRevision(ctx, tx, templateID)
	})
}

// CustomerTemplate returns only an internally approved revision owned by this customer.
func CustomerTemplate(ctx context.Context, pool *pgxpool.Pool, jobID, revisionID, identity string) (*TemplateRevision, error) {
	session, err := portal.ResolveSession(ctx, pool, identity)
	if err != nil {
		return nil, ErrNotFound
	}
	var visible bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_template_revision t
 JOIN fabrication_revision_approval a ON a.template_id=t.template_id JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id
 WHERE j.fabrication_job_uuid=$1 AND j.fabrication_job_customer_id=$2 AND j.fabrication_job_deleted_at IS NULL AND t.template_uuid=$3
 AND t.state IN ('submitted','approved') AND (t.change_summary->>'customerRequired')::boolean AND a.internal_approved_at IS NOT NULL AND a.rejected_at IS NULL)`, jobID, session.CustomerID, revisionID).Scan(&visible)
	if err != nil {
		return nil, fmt.Errorf("check customer template: %w", err)
	}
	if !visible {
		return nil, ErrNotFound
	}
	revisions, err := Templates(ctx, pool, jobID)
	if err != nil {
		return nil, err
	}
	for _, r := range revisions {
		if r.ID == revisionID {
			return &r, nil
		}
	}
	return nil, ErrNotFound
}
