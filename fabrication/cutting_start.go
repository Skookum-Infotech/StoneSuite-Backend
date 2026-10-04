package fabrication

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
)

// StartCuttingInput selects all pieces in the reviewed layouts of these slabs.
type StartCuttingInput struct {
	CommandMeta
	TemplateID string   `json:"templateId"`
	BinID      string   `json:"binId"`
	SlabIDs    []string `json:"slabIds"`
}

// StartCuttingRun persists approved cutting evidence without posting consumption.
// HTTP exposure awaits production-piece binding and completion integration.
func StartCuttingRun(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in StartCuttingInput) (ActionResult, error) {
	actor.permission = authz.ActionUpdate
	return startCuttingRun(ctx, pool, jobID, actor, in)
}

func startCuttingRun(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in StartCuttingInput) (ActionResult, error) {
	if actor.EmployeeID <= 0 {
		return ActionResult{}, ErrActionPermission
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return ActionResult{}, fmt.Errorf("encode cutting start: %w", err)
	}
	var runUUID string
	return executeAction(ctx, pool, jobID, actor, in.CommandMeta, actionCommand{Code: "start_cutting_run", SubjectID: jobID, Payload: payload, MovementPermission: actor.permission != "", RelatedID: &runUUID}, func(ctx context.Context, tx pgx.Tx, state actionState) error {
		job, err := lockJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if IsTerminal(job.statusCode) || job.statusCode == StatusOnHold {
			return ClientError{Msg: "Resume an active job before starting cutting."}
		}
		materials, err := lockCuttingMaterials(ctx, tx, state.JobID, in.TemplateID, in.BinID, in.SlabIDs)
		if err != nil {
			return err
		}
		var runID int64
		err = tx.QueryRow(ctx, `INSERT INTO fabrication_cutting_run(fabrication_job_id,template_id,inventory_bin_id,started_by)
  SELECT $1,t.template_id,b.inventory_bin_id,$4 FROM fabrication_template_revision t,inventory_bin b
  WHERE t.template_uuid=$2 AND b.inventory_bin_uuid=$3 RETURNING cutting_run_id,cutting_run_uuid`, state.JobID, in.TemplateID, in.BinID, actor.EmployeeID).Scan(&runID, &runUUID)
		if err != nil {
			return fmt.Errorf("create cutting run: %w", err)
		}
		for _, material := range materials {
			line, err := json.Marshal(material.Line)
			if err != nil {
				return fmt.Errorf("encode cutting line: %w", err)
			}
			layout, err := json.Marshal(material.Layout)
			if err != nil {
				return fmt.Errorf("encode cutting layout: %w", err)
			}
			_, err = tx.Exec(ctx, `INSERT INTO fabrication_cutting_input(cutting_run_id,fabrication_job_slab_id,source_line_uuid,measured_line,layout) VALUES($1,$2,$3,$4,$5)`, runID, material.AllocationID, material.Line.SourceLineID, line, layout)
			if isUniqueViolation(err) {
				return ErrActionConflict
			}
			if err != nil {
				return fmt.Errorf("save cutting input: %w", err)
			}
			for _, placement := range material.Layout.Placements {
				pieceID, err := createCuttingPiece(ctx, tx, state.JobID, actor.EmployeeID, material.Line.Pieces[placement.PieceIndex])
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `INSERT INTO fabrication_cutting_selection(cutting_run_id,template_id,source_line_uuid,piece_index,fabrication_job_item_id) SELECT $1,template_id,$2,$3,$4 FROM fabrication_cutting_run WHERE cutting_run_id=$1`, runID, material.Line.SourceLineID, placement.PieceIndex, pieceID)
				if isUniqueViolation(err) {
					return ErrActionConflict
				}
				if err != nil {
					return fmt.Errorf("reserve cutting piece: %w", err)
				}
			}
		}
		return nil
	})
}

func requireUnstartedMaterial(ctx context.Context, tx pgx.Tx, jobID, slabID int) error {
	var started bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_cutting_input i JOIN fabrication_job_slab a USING(fabrication_job_slab_id) WHERE a.fabrication_job_id=$1 AND ($2=0 OR a.inventory_slab_id=$2))`, jobID, slabID).Scan(&started)
	if err != nil {
		return fmt.Errorf("check started cutting material: %w", err)
	}
	if started {
		return ClientError{Msg: "Started cutting material requires completion or measured disposition; it cannot be released or moved as untouched."}
	}
	return nil
}
