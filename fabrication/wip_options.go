package fabrication

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
)

// WIPOption is an allocated unit with its server-evaluated movement action.
type WIPOption struct {
	WarehouseID string          `json:"warehouseId"`
	SlabID      string          `json:"slabId"`
	Serial      string          `json:"serial"`
	Action      AvailableAction `json:"action"`
}

// WIPOptions returns movement affordances; callers must authorize job and inventory reads.
func WIPOptions(ctx context.Context, pool *pgxpool.Pool, jobID, identity string) ([]WIPOption, error) {
	job, err := Get(ctx, pool, jobID)
	if err != nil {
		return nil, err
	}
	decision, err := authz.Check(ctx, pool, identity, authz.ResourceInstallation, authz.ActionUpdate)
	if err != nil {
		return nil, fmt.Errorf("check WIP job permission: %w", err)
	}
	movement, err := authz.Check(ctx, pool, identity, authz.ResourceInventoryUnit, authz.ActionUpdate)
	if err != nil {
		return nil, fmt.Errorf("check WIP inventory permission: %w", err)
	}
	// The mutation performs a fresh ownership check. Match own-scope affordances here.
	var owns bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_job j JOIN employee e ON e.employee_id=j.job_owner_id JOIN users u ON u.id=e.employee_user_id WHERE j.fabrication_job_uuid=$1 AND u.identity_id=$2)`, jobID, identity).Scan(&owns)
	if err != nil {
		return nil, fmt.Errorf("check WIP ownership: %w", err)
	}
	if !decision.Allowed || !movement.Allowed || (decision.Scope != authz.ScopeAll && !owns) {
		return []WIPOption{}, nil
	}
	var ready bool
	err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_template_revision t JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id JOIN sales_order so ON so.sales_order_id=j.sales_order_id WHERE j.fabrication_job_uuid=$1 AND t.state='approved' AND t.sales_order_version=so.sales_order_record_version AND so.sales_order_deleted_at IS NULL)`, jobID).Scan(&ready)
	if err != nil {
		return nil, fmt.Errorf("read WIP approval: %w", err)
	}
	rows, err := pool.Query(ctx, `SELECT s.inventory_slab_uuid,s.slab_serial,s.inspection_status,w.company_location_uuid,EXISTS(SELECT 1 FROM fabrication_material_layout l JOIN fabrication_template_revision t ON t.template_id=l.template_id WHERE l.fabrication_job_slab_id=a.fabrication_job_slab_id AND t.fabrication_job_id=a.fabrication_job_id AND t.state='approved') FROM fabrication_job_slab a JOIN fabrication_job j ON j.fabrication_job_id=a.fabrication_job_id JOIN inventory_slab s ON s.inventory_slab_id=a.inventory_slab_id JOIN company_location w ON w.company_location_id=s.warehouse_id WHERE j.fabrication_job_uuid=$1 AND a.allocation_status='reserved' AND s.slab_status='reserved' AND s.slab_deleted_at IS NULL ORDER BY s.slab_serial`, jobID)
	if err != nil {
		return nil, fmt.Errorf("read WIP materials: %w", err)
	}
	defer rows.Close()
	out := []WIPOption{}
	for rows.Next() {
		var option WIPOption
		var inspection string
		var reviewed bool
		if err = rows.Scan(&option.SlabID, &option.Serial, &inspection, &option.WarehouseID, &reviewed); err != nil {
			return nil, fmt.Errorf("scan WIP material: %w", err)
		}
		option.Action = AvailableAction{Code: "transfer_to_wip", Label: "Move to WIP", Enabled: true, InputType: "wip_transfer"}
		if !ready {
			option.Action.Blockers = append(option.Action.Blockers, ActionBlocker{Code: "template_approval", Message: "Approve the current template before moving material."})
		}
		if !reviewed {
			option.Action.Blockers = append(option.Action.Blockers, ActionBlocker{Code: "material_layout", Message: "Allocate material with a reviewed layout on the current template."})
		}
		if inspection != "accepted" {
			option.Action.Blockers = append(option.Action.Blockers, ActionBlocker{Code: "inspection", Message: "Inspect and accept this slab first."})
		}
		if job.WorkflowVersion != WorkflowPieceActions || job.StatusCode == StatusOnHold || IsTerminal(job.StatusCode) {
			option.Action.Blockers = append(option.Action.Blockers, ActionBlocker{Code: "job_state", Message: "An active piece-workflow job is required."})
		}
		option.Action.Enabled = len(option.Action.Blockers) == 0
		out = append(out, option)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read WIP materials: %w", err)
	}
	return out, nil
}
