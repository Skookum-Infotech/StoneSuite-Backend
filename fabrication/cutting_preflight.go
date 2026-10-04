package fabrication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"stonesuite-backend/inventory"
)

// cuttingMaterial is locked evidence for a run; it is never accepted from a client.
type cuttingMaterial struct {
	SlabID, AllocationID int
	SlabUUID             string
	Line                 TemplateLine
	Layout               MaterialLayout
}

// lockCuttingMaterials is a preflight building block, not a start command.
// The caller must lock/authorize the job first and retain this transaction
// through run creation. Run/piece exclusivity remains the start command's job.
func lockCuttingMaterials(ctx context.Context, tx pgx.Tx, jobID int, templateUUID, binUUID string, slabUUIDs []string) ([]cuttingMaterial, error) {
	if len(slabUUIDs) == 0 {
		return nil, ClientError{Msg: "Choose at least one slab for the cutting run."}
	}
	ids := append([]string(nil), slabUUIDs...)
	for i := range ids {
		ids[i] = strings.ToLower(ids[i])
	}
	sort.Strings(ids)
	for i, value := range append([]string{templateUUID, binUUID}, ids...) {
		var uuid pgtype.UUID
		if err := uuid.Scan(value); err != nil || !uuid.Valid {
			return nil, ClientError{Msg: "Choose valid template, WIP bin and slab IDs."}
		}
		if i >= 3 && ids[i-2] == ids[i-3] {
			return nil, ClientError{Msg: "A slab can appear only once in a cutting run."}
		}
	}
	var templateID int64
	var approved, current int64
	var raw []byte
	err := tx.QueryRow(ctx, `SELECT t.template_id,t.sales_order_version,so.sales_order_record_version,t.measured_lines
 FROM fabrication_template_revision t JOIN fabrication_job j ON j.fabrication_job_id=t.fabrication_job_id
 JOIN sales_order so ON so.sales_order_id=j.sales_order_id
 WHERE t.fabrication_job_id=$1 AND t.template_uuid=$2 AND t.state='approved' AND so.sales_order_deleted_at IS NULL
 FOR SHARE OF t,so`, jobID, templateUUID).Scan(&templateID, &approved, &current, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrApprovalRequired
	}
	if err != nil {
		return nil, fmt.Errorf("load cutting approval: %w", err)
	}
	if approved != current {
		return nil, ErrActionConflict
	}
	var lines []TemplateLine
	if err = json.Unmarshal(raw, &lines); err != nil {
		return nil, fmt.Errorf("decode cutting template: %w", err)
	}
	var binID, warehouseID int
	var active, wip bool
	var path string
	err = tx.QueryRow(ctx, `SELECT inventory_bin_id,warehouse_id,bin_is_active,bin_is_wip,bin_path FROM inventory_bin
 WHERE inventory_bin_uuid=$1 AND bin_deleted_at IS NULL FOR SHARE`, binUUID).Scan(&binID, &warehouseID, &active, &wip, &path)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Choose an active WIP bin."}
	}
	if err != nil {
		return nil, fmt.Errorf("lock cutting WIP bin: %w", err)
	}
	if !active || !wip {
		return nil, ClientError{Msg: "Choose an active WIP bin."}
	}
	if err = inventory.CheckNotFrozenByCount(ctx, tx, warehouseID, path); err != nil {
		if inventory.IsClientError(err) {
			return nil, ClientError{Msg: err.Error()}
		}
		return nil, fmt.Errorf("check cutting count freeze: %w", err)
	}
	out := make([]cuttingMaterial, 0, len(ids))
	for _, id := range ids {
		material, err := lockCuttingMaterial(ctx, tx, jobID, templateID, binID, warehouseID, id, lines)
		if err != nil {
			return nil, err
		}
		out = append(out, material)
	}
	return out, nil
}

func lockCuttingMaterial(ctx context.Context, tx pgx.Tx, jobID int, templateID int64, binID, warehouseID int, uuid string, lines []TemplateLine) (cuttingMaterial, error) {
	var out cuttingMaterial
	out.SlabUUID = uuid
	var status, inspection string
	var actualBin *int
	var actualWarehouse int
	var bundleID *int
	var surface MaterialSurface
	err := tx.QueryRow(ctx, `SELECT s.inventory_slab_id,s.slab_status,s.inspection_status,s.inventory_bin_id,s.warehouse_id,s.inventory_bundle_id,
 i.inventory_item_uuid::text,s.slab_finish,s.slab_length_mm,s.slab_width_mm,s.slab_thickness_mm
 FROM inventory_slab s JOIN inventory_item i ON i.inventory_item_id=s.inventory_item_id
 WHERE s.inventory_slab_uuid=$1 AND s.slab_deleted_at IS NULL AND i.inventory_item_deleted_at IS NULL FOR UPDATE OF s`, uuid).Scan(
		&out.SlabID, &status, &inspection, &actualBin, &actualWarehouse, &bundleID, &surface.MaterialID, &surface.Finish, &surface.LengthMM, &surface.WidthMM, &surface.ThicknessMM)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ErrNotFound
	}
	if err != nil {
		return out, fmt.Errorf("lock cutting slab: %w", err)
	}
	if status != "reserved" || inspection != "accepted" {
		return out, ClientError{Msg: "Cutting requires reserved material with accepted inspection."}
	}
	if actualBin == nil || *actualBin != binID || actualWarehouse != warehouseID {
		return out, ClientError{Msg: "Transfer every selected slab into this WIP bin before cutting."}
	}
	if bundleID != nil {
		var bundleStatus string
		if err = tx.QueryRow(ctx, `SELECT bundle_status FROM inventory_bundle WHERE inventory_bundle_id=$1 FOR SHARE`, *bundleID).Scan(&bundleStatus); err != nil {
			return out, fmt.Errorf("lock cutting bundle: %w", err)
		}
		if bundleStatus == inventory.BundleSealed {
			return out, ClientError{Msg: "Break the sealed bundle before cutting."}
		}
	}
	var lineID string
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT a.fabrication_job_slab_id,l.source_line_uuid::text,l.layout
 FROM fabrication_job_slab a JOIN fabrication_material_layout l USING(fabrication_job_slab_id)
 WHERE a.fabrication_job_id=$1 AND a.inventory_slab_id=$2 AND a.allocation_status='reserved' AND l.template_id=$3
 FOR UPDATE OF a FOR SHARE OF l`, jobID, out.SlabID, templateID).Scan(&out.AllocationID, &lineID, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, ClientError{Msg: "Reserve the slab with a reviewed layout on the approved template for this job."}
	}
	if err != nil {
		return out, fmt.Errorf("lock cutting layout: %w", err)
	}
	if err = json.Unmarshal(raw, &out.Layout); err != nil {
		return out, fmt.Errorf("decode cutting layout: %w", err)
	}
	for _, line := range lines {
		if line.SourceLineID == lineID {
			out.Line = line
			return out, ValidateMaterialLayout(surface, line, out.Layout)
		}
	}
	return out, ClientError{Msg: "The allocated line is missing from the approved template."}
}
