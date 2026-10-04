package fabrication

import (
	"context"
	"encoding/json"
	"fmt"
	"stonesuite-backend/authz"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SubmitTemplate validates the caller's current grant and stores an immutable revision.
func SubmitTemplate(ctx context.Context, pool *pgxpool.Pool, jobUUID string, actor ActionActor, in SubmitTemplateInput) (ActionResult, error) {
	actor.permission = authz.ActionUpdate
	return submitTemplate(ctx, pool, jobUUID, actor, in)
}

func submitTemplate(ctx context.Context, pool *pgxpool.Pool, jobUUID string, actor ActionActor, in SubmitTemplateInput) (ActionResult, error) {
	payload, err := json.Marshal(in)
	if err != nil {
		return ActionResult{}, ClientError{Msg: "Invalid template measurements."}
	}
	return executeAction(ctx, pool, jobUUID, actor, in.CommandMeta, actionCommand{Code: "submit_template", SubjectID: jobUUID, Payload: payload}, func(ctx context.Context, tx pgx.Tx, s actionState) error {
		var soID int
		var version int64
		var frozen bool
		err := tx.QueryRow(ctx, `SELECT so.sales_order_id,so.sales_order_record_version FROM sales_order so JOIN fabrication_job fj ON fj.sales_order_id=so.sales_order_id WHERE fj.fabrication_job_id=$1 AND so.sales_order_deleted_at IS NULL FOR UPDATE OF so`, s.JobID).Scan(&soID, &version)
		if err != nil {
			return fmt.Errorf("load template order: %w", err)
		}
		if in.SalesOrderVersion != version {
			return ErrActionConflict
		}
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_job_item WHERE fabrication_job_id=$1 AND production_stage NOT IN ('planned','ready_cutting') AND item_deleted_at IS NULL)`, s.JobID).Scan(&frozen)
		if err != nil {
			return fmt.Errorf("check cut template: %w", err)
		}
		if frozen {
			return ClientError{Msg: "Production has begun; use a remake request for changed pieces."}
		}
		baseline, err := templateBaseline(ctx, tx, soID)
		if err != nil {
			return err
		}
		allowed := make(map[string]bool, len(baseline))
		for _, line := range baseline {
			allowed[line.SourceLineID] = true
		}
		for _, line := range in.Lines {
			if !allowed[line.SourceLineID] {
				return ClientError{Msg: "A measured line does not belong to this sales order."}
			}
			var live bool
			err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM inventory_item WHERE inventory_item_uuid::text=$1 AND inventory_item_deleted_at IS NULL)`, line.MaterialID).Scan(&live)
			if err != nil {
				return fmt.Errorf("validate template material: %w", err)
			}
			if !live {
				return ClientError{Msg: "Select an available catalog material."}
			}
		}
		change, err := CompareTemplate(baseline, in.Lines)
		if err != nil {
			return err
		}
		baseJSON, err := json.Marshal(baseline)
		if err != nil {
			return fmt.Errorf("encode template baseline: %w", err)
		}
		linesJSON, err := json.Marshal(in.Lines)
		if err != nil {
			return fmt.Errorf("encode template measurements: %w", err)
		}
		changeJSON, err := json.Marshal(change)
		if err != nil {
			return fmt.Errorf("encode template changes: %w", err)
		}
		_, err = tx.Exec(ctx, `UPDATE fabrication_template_revision SET state='superseded' WHERE fabrication_job_id=$1 AND state IN ('submitted','approved')`, s.JobID)
		if err != nil {
			return fmt.Errorf("supersede template: %w", err)
		}
		var templateID int64
		err = tx.QueryRow(ctx, `INSERT INTO fabrication_template_revision(fabrication_job_id,revision,sales_order_version,baseline,measured_lines,change_summary,created_by)
 SELECT $1,COALESCE(MAX(revision),0)+1,$2,$3,$4,$5,$6 FROM fabrication_template_revision WHERE fabrication_job_id=$1 RETURNING template_id`, s.JobID, version, baseJSON, linesJSON, changeJSON, nullableInt(actor.EmployeeID)).Scan(&templateID)
		if err != nil {
			return fmt.Errorf("submit template revision: %w", err)
		}
		return snapshotTemplateApprovers(ctx, tx, templateID)
	})
}

func templateBaseline(ctx context.Context, tx pgx.Tx, soID int) ([]TemplateLine, error) {
	rows, err := tx.Query(ctx, `SELECT l.sales_order_item_uuid,COALESCE(i.inventory_item_uuid::text,''),l.quantity,l.unit_price,l.description
 FROM sales_order_item l LEFT JOIN inventory_item i ON i.inventory_item_id=l.inventory_item_id
 WHERE l.sales_order_id=$1 AND l.item_deleted_at IS NULL ORDER BY l.line_number`, soID)
	if err != nil {
		return nil, fmt.Errorf("load template baseline: %w", err)
	}
	defer rows.Close()
	out := []TemplateLine{}
	for rows.Next() {
		var line TemplateLine
		if err := rows.Scan(&line.SourceLineID, &line.MaterialID, &line.Quantity, &line.UnitPrice, &line.Scope); err != nil {
			return nil, fmt.Errorf("scan template baseline: %w", err)
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

// Templates returns immutable submitted revisions after the caller's scope check.
func Templates(ctx context.Context, pool *pgxpool.Pool, jobUUID string) ([]TemplateRevision, error) {
	rows, err := pool.Query(ctx, `SELECT t.template_uuid,t.revision,t.sales_order_version,t.state,t.baseline,t.measured_lines,t.change_summary,t.created_at,j.fabrication_job_record_version
 FROM fabrication_template_revision t JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id
 WHERE j.fabrication_job_uuid=$1 AND j.fabrication_job_deleted_at IS NULL ORDER BY t.revision DESC`, jobUUID)
	if err != nil {
		return nil, fmt.Errorf("list template revisions: %w", err)
	}
	defer rows.Close()
	out := []TemplateRevision{}
	for rows.Next() {
		var r TemplateRevision
		var baseline, lines, change []byte
		if err := rows.Scan(&r.ID, &r.Revision, &r.SalesOrderVersion, &r.State, &baseline, &lines, &change, &r.CreatedAt, &r.JobVersion); err != nil {
			return nil, fmt.Errorf("scan template: %w", err)
		}
		if err := json.Unmarshal(baseline, &r.Baseline); err != nil {
			return nil, fmt.Errorf("decode baseline: %w", err)
		}
		if err := json.Unmarshal(lines, &r.Lines); err != nil {
			return nil, fmt.Errorf("decode template lines: %w", err)
		}
		if err := json.Unmarshal(change, &r.Change); err != nil {
			return nil, fmt.Errorf("decode template changes: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
