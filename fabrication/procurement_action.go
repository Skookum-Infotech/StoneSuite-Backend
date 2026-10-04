package fabrication

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
)

// ProcurementAction describes shortage purchasing for an already read-authorized job.
func ProcurementAction(ctx context.Context, pool *pgxpool.Pool, jobID, identity string, employee int) (AvailableAction, error) {
	job, err := Get(ctx, pool, jobID)
	if err != nil {
		return AvailableAction{}, err
	}
	update, err := authz.Check(ctx, pool, identity, authz.ResourceInstallation, authz.ActionUpdate)
	if err != nil {
		return AvailableAction{}, fmt.Errorf("check procurement job permission: %w", err)
	}
	purchase, err := authz.Check(ctx, pool, identity, authz.ResourcePurchaseOrder, authz.ActionCreate)
	if err != nil {
		return AvailableAction{}, fmt.Errorf("check procurement permission: %w", err)
	}
	var owns, ready bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_job j JOIN employee e ON e.employee_id=j.job_owner_id JOIN users u ON u.id=e.employee_user_id WHERE j.fabrication_job_uuid=$1 AND u.identity_id=$2), EXISTS(SELECT 1 FROM fabrication_template_revision t JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id JOIN sales_order so ON so.sales_order_id=j.sales_order_id WHERE j.fabrication_job_uuid=$1 AND t.state='approved' AND t.sales_order_version=so.sales_order_record_version AND so.sales_order_deleted_at IS NULL)`, jobID, identity).Scan(&owns, &ready)
	if err != nil {
		return AvailableAction{}, fmt.Errorf("read procurement readiness: %w", err)
	}
	return shortagePurchaseAction(job.WorkflowVersion == WorkflowPieceActions && job.StatusCode != StatusOnHold && !IsTerminal(job.StatusCode), update.Allowed && (update.Scope == authz.ScopeAll || owns), purchase.Allowed, employee > 0, ready), nil
}

func shortagePurchaseAction(active, update, purchase, employee, ready bool) AvailableAction {
	action := AvailableAction{Code: "create_shortage_po", Label: "Create linked draft purchase order", InputType: "shortage_purchase", Blockers: []ActionBlocker{}}
	checks := []struct {
		valid         bool
		code, message string
	}{
		{active, "job_state", "Resume an active piece-workflow job before purchasing material."},
		{update, "job_permission", "Job update access is required to purchase missing material."},
		{purchase, "purchase_permission", "Purchase order create access is required."},
		{employee, "employee_required", "Link your account to an employee before creating a purchase order."},
		{ready, "template_approval", "Approve a template matching the current sales order before purchasing material."},
	}
	for _, check := range checks {
		if !check.valid {
			action.Blockers = append(action.Blockers, ActionBlocker{Code: check.code, Message: check.message})
		}
	}
	action.Enabled = len(action.Blockers) == 0
	return action
}
