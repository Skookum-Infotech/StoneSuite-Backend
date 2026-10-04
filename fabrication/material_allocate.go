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
)

// AllocateMaterialInput reserves a slab for measured pieces of an approved line.
type AllocateMaterialInput struct {
	CommandMeta
	SlabID       string         `json:"slabId"`
	TemplateID   string         `json:"templateId"`
	SourceLineID string         `json:"sourceLineId"`
	Layout       MaterialLayout `json:"layout"`
}

// AllocateMaterial rechecks job and inventory permissions before reserving stock.
func AllocateMaterial(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in AllocateMaterialInput) (ActionResult, error) {
	actor.permission = authz.ActionUpdate
	return allocateMaterial(ctx, pool, jobID, actor, in)
}

func allocateMaterial(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in AllocateMaterialInput) (ActionResult, error) {
	for _, value := range []string{in.SlabID, in.TemplateID, in.SourceLineID} {
		var id pgtype.UUID
		if err := id.Scan(value); err != nil || !id.Valid {
			return ActionResult{}, ClientError{Msg: "Choose a valid slab, template revision and source line."}
		}
	}
	if actor.EmployeeID <= 0 {
		return ActionResult{}, ErrActionPermission
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return ActionResult{}, ClientError{Msg: "Invalid material layout."}
	}
	return executeAction(ctx, pool, jobID, actor, in.CommandMeta, actionCommand{Code: "allocate_material", SubjectID: in.SlabID, Payload: payload, MovementPermission: actor.permission != ""}, func(ctx context.Context, tx pgx.Tx, state actionState) error {
		st, err := lockJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if st.statusCode == StatusOnHold || IsTerminal(st.statusCode) {
			return ClientError{Msg: "Resume an active job before allocating material."}
		}
		var templateID int64
		var approved, current int64
		var data []byte
		err = tx.QueryRow(ctx, `SELECT t.template_id,t.sales_order_version,so.sales_order_record_version,t.measured_lines FROM fabrication_template_revision t JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id JOIN sales_order so ON so.sales_order_id=j.sales_order_id WHERE t.fabrication_job_id=$1 AND t.template_uuid=$2 AND t.state='approved' AND so.sales_order_deleted_at IS NULL FOR SHARE OF t,so`, state.JobID, in.TemplateID).Scan(&templateID, &approved, &current, &data)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrApprovalRequired
		}
		if err != nil {
			return fmt.Errorf("load allocation template: %w", err)
		}
		if approved != current {
			return ErrActionConflict
		}
		var lines []TemplateLine
		if err = json.Unmarshal(data, &lines); err != nil {
			return fmt.Errorf("decode allocation template: %w", err)
		}
		var line *TemplateLine
		for i := range lines {
			if lines[i].SourceLineID == in.SourceLineID {
				line = &lines[i]
				break
			}
		}
		if line == nil {
			return ClientError{Msg: "Choose a measured line on this approved template."}
		}
		var slabID int
		var status, inspection string
		var surface MaterialSurface
		err = tx.QueryRow(ctx, `SELECT s.inventory_slab_id,s.slab_status,s.inspection_status,i.inventory_item_uuid::text,s.slab_finish,s.slab_length_mm,s.slab_width_mm,s.slab_thickness_mm FROM inventory_slab s JOIN inventory_item i ON i.inventory_item_id=s.inventory_item_id WHERE s.inventory_slab_uuid=$1 AND s.slab_deleted_at IS NULL AND i.inventory_item_deleted_at IS NULL FOR UPDATE OF s`, in.SlabID).Scan(&slabID, &status, &inspection, &surface.MaterialID, &surface.Finish, &surface.LengthMM, &surface.WidthMM, &surface.ThicknessMM)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock layout slab: %w", err)
		}
		if status != "available" || inspection != "accepted" {
			return ClientError{Msg: "Choose an available slab or remnant with accepted inspection."}
		}
		if err = ValidateMaterialLayout(surface, *line, in.Layout); err != nil {
			return err
		}
		if err = checkAllocatedPlacements(ctx, tx, templateID, in.SourceLineID, in.Layout); err != nil {
			return err
		}
		var allocationID int
		err = tx.QueryRow(ctx, `INSERT INTO fabrication_job_slab(fabrication_job_id,inventory_slab_id,allocation_status,reserved_by) VALUES($1,$2,'reserved',$3) RETURNING fabrication_job_slab_id`, state.JobID, slabID, actor.EmployeeID).Scan(&allocationID)
		if isUniqueViolation(err) {
			return ErrActionConflict
		}
		if err != nil {
			return fmt.Errorf("reserve layout slab: %w", err)
		}
		layout, err := json.Marshal(in.Layout)
		if err != nil {
			return fmt.Errorf("encode saved layout: %w", err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO fabrication_material_layout(fabrication_job_slab_id,template_id,source_line_uuid,layout,reviewed_by) VALUES($1,$2,$3,$4,$5)`, allocationID, templateID, in.SourceLineID, layout, actor.EmployeeID); err != nil {
			return fmt.Errorf("save allocation layout: %w", err)
		}
		if _, err = tx.Exec(ctx, `UPDATE inventory_slab SET slab_status='reserved',slab_updated_at=NOW() WHERE inventory_slab_id=$1`, slabID); err != nil {
			return fmt.Errorf("mark layout slab reserved: %w", err)
		}
		return nil
	})
}

func checkAllocatedPlacements(ctx context.Context, tx pgx.Tx, templateID int64, lineID string, layout MaterialLayout) error {
	rows, err := tx.Query(ctx, `SELECT l.layout FROM fabrication_material_layout l JOIN fabrication_job_slab a ON a.fabrication_job_slab_id=l.fabrication_job_slab_id WHERE l.template_id=$1 AND l.source_line_uuid=$2 AND a.allocation_status IN ('reserved','consumed')`, templateID, lineID)
	if err != nil {
		return fmt.Errorf("load assigned placements: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			return fmt.Errorf("read assigned placements: %w", err)
		}
		var previous MaterialLayout
		if err = json.Unmarshal(raw, &previous); err != nil {
			return fmt.Errorf("decode assigned placements: %w", err)
		}
		for _, a := range previous.Placements {
			for _, b := range layout.Placements {
				if a.PieceIndex == b.PieceIndex {
					return ClientError{Msg: "A selected piece already has a reserved or consumed slab."}
				}
			}
		}
	}
	if err = rows.Err(); err != nil {
		return fmt.Errorf("iterate assigned placements: %w", err)
	}
	return nil
}
