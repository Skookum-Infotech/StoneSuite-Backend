// bintransfer/store_read.go — the base SELECTs, row scans and Get.
package bintransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/workflow"
)

// headerSelect is the base SELECT shared by Get and Search. Column order must
// match scanHeader's Scan(...) exactly; the table alias `bt` matches
// resolver.go's field expressions.
const headerSelect = `
	SELECT bt.bin_transfer_uuid, COALESCE(bt.bin_transfer_number,''),
	       rs.record_status_code, rs.record_status_name, bt.bin_transfer_approval_status,
	       bt.warehouse_id, COALESCE(w.name,''),
	       to_char(bt.bin_transfer_date, 'YYYY-MM-DD'), bt.bin_transfer_notes,
	       COALESCE(ou.id::text,''), bt.bin_transfer_owner_id,
	       COALESCE(NULLIF(TRIM(COALESCE(oe.employee_first_name,'') || ' ' ||
	                            COALESCE(oe.employee_last_name,'')), ''), ''),
	       to_char(bt.bin_transfer_completed_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
	       COALESCE(NULLIF(TRIM(COALESCE(ce.employee_first_name,'') || ' ' ||
	                            COALESCE(ce.employee_last_name,'')), ''), ''),
	       to_char(bt.bin_transfer_cancelled_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
	       bt.bin_transfer_cancel_reason,
	       (SELECT COUNT(*) FROM bin_transfer_line l
	         WHERE l.bin_transfer_id = bt.bin_transfer_id AND l.line_deleted_at IS NULL),
	       (SELECT COALESCE(SUM(s.slab_area), 0) FROM bin_transfer_line l
	         JOIN inventory_slab s ON s.inventory_slab_id = l.inventory_slab_id
	         WHERE l.bin_transfer_id = bt.bin_transfer_id AND l.line_deleted_at IS NULL),
	       COALESCE((SELECT CASE WHEN COUNT(DISTINCT l.to_bin_id) = 1 THEN MIN(tb.bin_path)
	                             ELSE COUNT(DISTINCT l.to_bin_id)::text || ' bins' END
	         FROM bin_transfer_line l JOIN inventory_bin tb ON tb.inventory_bin_id = l.to_bin_id
	         WHERE l.bin_transfer_id = bt.bin_transfer_id AND l.line_deleted_at IS NULL), ''),
	       bt.bin_transfer_custom_fields,
	       bt.bin_transfer_created_at, bt.bin_transfer_updated_at, bt.bin_transfer_record_version,
	       bt.bin_transfer_id, bt.bin_transfer_status
	FROM bin_transfer bt
	JOIN lkp_record_status rs   ON rs.record_status_id = bt.bin_transfer_status
	JOIN company_location w     ON w.company_location_id = bt.warehouse_id
	LEFT JOIN employee oe       ON oe.employee_id = bt.bin_transfer_owner_id
	LEFT JOIN users ou          ON ou.id = oe.employee_user_id
	LEFT JOIN employee ce       ON ce.employee_id = bt.bin_transfer_completed_by`

// headerMeta carries the internal numeric ids a row has but the API does not
// expose. Search needs the status id to mint a cursor for a status sort.
type headerMeta struct {
	internalID int
	statusID   int
}

func scanHeader(row pgx.Row) (*BinTransfer, headerMeta, error) {
	var (
		t      BinTransfer
		custom map[string]any
		meta   headerMeta
	)
	err := row.Scan(
		&t.ID, &t.Number,
		&t.StatusCode, &t.StatusName, &t.ApprovalStatus,
		&t.WarehouseID, &t.WarehouseName,
		&t.Date, &t.Notes,
		&t.OwnerUserID, &t.OwnerEmployeeID, &t.OwnerName,
		&t.CompletedAt, &t.CompletedByName,
		&t.CancelledAt, &t.CancelReason,
		&t.SlabCount, &t.TotalArea, &t.ToBinSummary,
		&custom,
		&t.CreatedAt, &t.UpdatedAt, &t.RecordVersion,
		&meta.internalID, &meta.statusID,
	)
	if err != nil {
		return nil, headerMeta{}, err
	}
	// NOT NULL DEFAULT '{}', but never hand a nil map back to callers.
	if custom == nil {
		custom = map[string]any{}
	}
	t.CustomFields = custom
	t.Lines = []Line{}
	t.NextStatusCodes = []string{}
	return &t, meta, nil
}

const lineSelect = `
	SELECT l.bin_transfer_line_id::text, l.line_number,
	       s.inventory_slab_uuid, s.slab_serial, s.slab_status,
	       COALESCE(ii.inventory_item_name,''), s.slab_area, COALESCE(au.unit_code,''),
	       fb.inventory_bin_uuid::text, COALESCE(fb.bin_path,''),
	       COALESCE(NULLIF(fb.bin_name,''), fb.bin_code, ''),
	       COALESCE(cb.bin_path,''),
	       tb.inventory_bin_uuid::text, tb.bin_path, COALESCE(NULLIF(tb.bin_name,''), tb.bin_code),
	       l.line_notes, to_char(l.line_moved_at, 'YYYY-MM-DD"T"HH24:MI:SS')
	FROM bin_transfer_line l
	JOIN inventory_slab s       ON s.inventory_slab_id = l.inventory_slab_id
	JOIN inventory_item ii      ON ii.inventory_item_id = s.inventory_item_id
	LEFT JOIN lkp_unit au       ON au.unit_id = s.slab_area_unit_id
	LEFT JOIN inventory_bin fb  ON fb.inventory_bin_id = l.from_bin_id
	LEFT JOIN inventory_bin cb  ON cb.inventory_bin_id = s.inventory_bin_id
	JOIN inventory_bin tb       ON tb.inventory_bin_id = l.to_bin_id
	WHERE l.bin_transfer_id = $1 AND l.line_deleted_at IS NULL
	ORDER BY l.line_number`

func loadLines(ctx context.Context, q workflow.Querier, transferID int) ([]Line, error) {
	rows, err := q.Query(ctx, lineSelect, transferID)
	if err != nil {
		return nil, fmt.Errorf("load bin transfer lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		if err := rows.Scan(
			&l.ID, &l.LineNumber,
			&l.UnitID, &l.UnitSerial, &l.UnitStatus,
			&l.InventoryItemName, &l.Area, &l.AreaUnitCode,
			&l.FromBinID, &l.FromBinPath, &l.FromBinName,
			&l.CurrentBinPath,
			&l.ToBinID, &l.ToBinPath, &l.ToBinName,
			&l.Notes, &l.MovedAt,
		); err != nil {
			return nil, fmt.Errorf("scan bin transfer line: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// fillNextStatusCodes sets NextStatusCodes on every transfer given from one read
// of the approver config -- a list passes every row at once so the counts are
// fetched once per request, not once per row.
func fillNextStatusCodes(ctx context.Context, q workflow.Querier, transfers ...*BinTransfer) error {
	if len(transfers) == 0 {
		return nil
	}
	recordTypeID, err := recordTypeIDByCode(ctx, q, recordTypeCode)
	if err != nil {
		return err
	}
	ungated, err := approvalchain.UngatedGates(ctx, q, moduleConfig(), recordTypeID)
	if err != nil {
		return err
	}
	for _, t := range transfers {
		t.NextStatusCodes = approvalchain.NextStatusCodes(t.StatusCode, NextStatuses, ungated)
	}
	return nil
}

// Get loads one live bin transfer (header + lines) by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*BinTransfer, error) {
	t, meta, err := scanHeader(pool.QueryRow(ctx, headerSelect+`
		WHERE bt.bin_transfer_uuid = $1 AND bt.bin_transfer_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) || (err != nil && isInvalidText(err)) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get bin transfer: %w", err)
	}
	if t.Lines, err = loadLines(ctx, pool, meta.internalID); err != nil {
		return nil, err
	}
	if err := fillNextStatusCodes(ctx, pool, t); err != nil {
		return nil, err
	}
	return t, nil
}
