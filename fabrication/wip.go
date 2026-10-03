package fabrication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
)

// ErrMovementPermission indicates missing inventory movement permission.
var ErrMovementPermission = errors.New("inventory movement permission required")

// WIPTransferInput moves one allocated slab to a shared or machine WIP bin.
type WIPTransferInput struct {
	CommandMeta
	SlabID string `json:"slabId"`
	BinID  string `json:"binId"`
	Note   string `json:"note"`
}

// TransferToWIP checks both job and inventory permissions, including on retries.
func TransferToWIP(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in WIPTransferInput) (ActionResult, error) {
	actor.permission = authz.ActionUpdate
	return transferToWIP(ctx, pool, jobID, actor, in)
}

func transferToWIP(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in WIPTransferInput) (ActionResult, error) {
	for _, value := range []string{in.SlabID, in.BinID} {
		var id pgtype.UUID
		if err := id.Scan(value); err != nil || !id.Valid {
			return ActionResult{}, ClientError{Msg: "Choose a valid slab and WIP bin."}
		}
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return ActionResult{}, fmt.Errorf("encode WIP transfer: %w", err)
	}
	return executeAction(ctx, pool, jobID, actor, in.CommandMeta, actionCommand{Code: "transfer_to_wip", SubjectID: in.SlabID, Payload: payload, MovementPermission: actor.permission != ""}, func(ctx context.Context, tx pgx.Tx, state actionState) error {
		st, err := lockJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if st.statusCode == StatusOnHold || IsTerminal(st.statusCode) {
			return ClientError{Msg: "Resume an active job before moving material into WIP."}
		}
		var approved, current int64
		err = tx.QueryRow(ctx, `SELECT t.sales_order_version,so.sales_order_record_version FROM fabrication_template_revision t JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id JOIN sales_order so ON so.sales_order_id=j.sales_order_id WHERE t.fabrication_job_id=$1 AND t.state='approved' AND so.sales_order_deleted_at IS NULL FOR SHARE OF t,so`, state.JobID).Scan(&approved, &current)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrApprovalRequired
		}
		if err != nil {
			return fmt.Errorf("check WIP template approval: %w", err)
		}
		if approved != current {
			return ErrActionConflict
		}
		var slabID int
		err = tx.QueryRow(ctx, `SELECT inventory_slab_id FROM inventory_slab WHERE inventory_slab_uuid=$1 AND slab_deleted_at IS NULL FOR UPDATE`, in.SlabID).Scan(&slabID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock WIP slab: %w", err)
		}
		if err = requireUnstartedMaterial(ctx, tx, state.JobID, slabID); err != nil {
			return err
		}
		var reserved bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_job_slab a JOIN inventory_slab s ON s.inventory_slab_id=a.inventory_slab_id WHERE a.fabrication_job_id=$1 AND s.inventory_slab_uuid=$2 AND a.allocation_status='reserved' AND s.slab_status='reserved' AND s.slab_deleted_at IS NULL)`, state.JobID, in.SlabID).Scan(&reserved)
		if err != nil {
			return fmt.Errorf("check WIP allocation: %w", err)
		}
		if !reserved {
			return ClientError{Msg: "Allocate this slab to the job before moving it into WIP."}
		}

		var reviewed bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_material_layout l JOIN fabrication_job_slab a ON a.fabrication_job_slab_id=l.fabrication_job_slab_id JOIN fabrication_template_revision t ON t.template_id=l.template_id WHERE a.fabrication_job_id=$1 AND a.inventory_slab_id=$2 AND a.allocation_status='reserved' AND t.fabrication_job_id=$1 AND t.state='approved')`, state.JobID, slabID).Scan(&reviewed)
		if err != nil {
			return fmt.Errorf("check WIP layout: %w", err)
		}
		if !reviewed {
			return ClientError{Msg: "Allocate material with a reviewed layout on the current approved template before moving into WIP."}
		}
		if err = inventory.MoveInspectedUnitToWIPTx(ctx, tx, in.SlabID, in.BinID, in.Note, actor.EmployeeID); err != nil {
			if inventory.IsClientError(err) {
				return ClientError{Msg: err.Error()}
			}
			return fmt.Errorf("transfer material to WIP: %w", err)
		}
		return nil
	})
}
