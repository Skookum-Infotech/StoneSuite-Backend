package fabrication

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPiecesLocked is returned when a piece add/edit/remove is attempted once
// the job can no longer accept scope-of-work changes (canEditPieces is
// false). Maps to HTTP 409 — the request is well-formed, just untimely.
var ErrPiecesLocked = errors.New("pieces can only be added, edited, or removed before the job reaches Cutting, and not while on hold")

// ErrPieceHasSlabs is returned when removing a piece that still has a live
// (reserved or consumed) slab allocation. Maps to HTTP 409.
var ErrPieceHasSlabs = errors.New("this piece has a live slab allocation; release or resolve it before removing the piece")

// AddPiece appends one fabricated piece to an existing job and seeds its
// piece-grain checklist steps (the gap the create path doesn't need to cover,
// since seedSteps runs once at Create for whatever pieces exist then).
// PieceNumber on the input is ignored — the number is always the next unused
// one for this job, computed under the job row's lock, so concurrent adds on
// the same job can't collide.
func AddPiece(ctx context.Context, pool *pgxpool.Pool, jobUUID string, in PieceInput, actorEmployeeID int) (*JobItem, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin add piece: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, jobUUID)
	if err != nil {
		return nil, err
	}
	if !canEditPieces(st.statusCode) {
		return nil, ErrPiecesLocked
	}

	var soInternalID int
	if err := tx.QueryRow(ctx, `
		SELECT sales_order_id FROM fabrication_job WHERE fabrication_job_id = $1`, st.internalID).Scan(&soInternalID); err != nil {
		return nil, fmt.Errorf("resolve job's sales order: %w", err)
	}

	soItemID, err := resolvePieceSalesOrderItem(ctx, tx, soInternalID, in.SalesOrderItemUUID)
	if err != nil {
		return nil, err
	}

	var pieceInternalID int
	var pieceUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO fabrication_job_item (
			fabrication_job_id, sales_order_item_id,
			piece_number, piece_name, piece_type,
			piece_length_mm, piece_width_mm, piece_thickness_mm,
			sink_cutout_count, cooktop_cutout_count, seam_count, item_created_by)
		SELECT $1, $2, COALESCE(MAX(piece_number), 0) + 1, $3, $4, $5, $6, $7, $8, $9, $10, $11
		FROM fabrication_job_item WHERE fabrication_job_id = $1
		RETURNING fabrication_job_item_id, fabrication_job_item_uuid`,
		st.internalID, soItemID, in.PieceName, in.PieceType,
		in.LengthMM, in.WidthMM, in.ThicknessMM,
		in.SinkCutoutCount, in.CooktopCutoutCount, in.SeamCount, nullableInt(actorEmployeeID)).Scan(&pieceInternalID, &pieceUUID)
	if err != nil {
		if isCheckViolation(err) {
			return nil, ClientError{Msg: "One or more piece values are out of range."}
		}
		return nil, fmt.Errorf("insert piece: %w", err)
	}

	if err := seedPieceSteps(ctx, tx, st.internalID, pieceInternalID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, st.internalID, "piece_add", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit add piece: %w", err)
	}
	return getPiece(ctx, pool, pieceUUID)
}

// UpdatePiece patches a piece's editable fields. The piece number is
// immutable once assigned (not part of the SET list) — it's a stable
// identifier other rows may reference by, not a display order to keep tidy.
func UpdatePiece(ctx context.Context, pool *pgxpool.Pool, jobUUID, pieceUUID string, in PieceInput, actorEmployeeID int) (*JobItem, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update piece: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, jobUUID)
	if err != nil {
		return nil, err
	}
	if !canEditPieces(st.statusCode) {
		return nil, ErrPiecesLocked
	}

	var soInternalID int
	if err := tx.QueryRow(ctx, `
		SELECT sales_order_id FROM fabrication_job WHERE fabrication_job_id = $1`, st.internalID).Scan(&soInternalID); err != nil {
		return nil, fmt.Errorf("resolve job's sales order: %w", err)
	}
	soItemID, err := resolvePieceSalesOrderItem(ctx, tx, soInternalID, in.SalesOrderItemUUID)
	if err != nil {
		return nil, err
	}

	ct, err := tx.Exec(ctx, `
		UPDATE fabrication_job_item SET
			sales_order_item_id = $3, piece_name = $4, piece_type = $5,
			piece_length_mm = $6, piece_width_mm = $7, piece_thickness_mm = $8,
			sink_cutout_count = $9, cooktop_cutout_count = $10, seam_count = $11
		WHERE fabrication_job_item_uuid = $1 AND fabrication_job_id = $2 AND item_deleted_at IS NULL`,
		pieceUUID, st.internalID, soItemID, in.PieceName, in.PieceType,
		in.LengthMM, in.WidthMM, in.ThicknessMM,
		in.SinkCutoutCount, in.CooktopCutoutCount, in.SeamCount)
	if err != nil {
		if isCheckViolation(err) {
			return nil, ClientError{Msg: "One or more piece values are out of range."}
		}
		return nil, fmt.Errorf("update piece: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, ClientError{Msg: "No such piece on this job."}
	}

	writeHistory(ctx, tx, st.internalID, "piece_update", nil, nil, actorEmployeeID)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update piece: %w", err)
	}
	return getPiece(ctx, pool, pieceUUID)
}

// RemovePiece soft-deletes a piece. Blocked while it still has a live slab
// allocation (release the slab, or record its cancel disposition, first) —
// removing it out from under an active allocation would orphan that row's
// meaning. Piece numbers are never reused or renumbered after a removal
// (uq_fab_item_piece_active only constrains still-live rows), so a gap in
// numbering is expected, not a bug.
func RemovePiece(ctx context.Context, pool *pgxpool.Pool, jobUUID, pieceUUID string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin remove piece: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, jobUUID)
	if err != nil {
		return err
	}
	if !canEditPieces(st.statusCode) {
		return ErrPiecesLocked
	}

	var pieceInternalID int
	err = tx.QueryRow(ctx, `
		SELECT fabrication_job_item_id FROM fabrication_job_item
		WHERE fabrication_job_item_uuid = $1 AND fabrication_job_id = $2 AND item_deleted_at IS NULL
		FOR UPDATE`, pieceUUID, st.internalID).Scan(&pieceInternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "No such piece on this job."}
	}
	if err != nil {
		return fmt.Errorf("lock piece for removal: %w", err)
	}

	var liveSlabs int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM fabrication_job_slab
		WHERE fabrication_job_item_id = $1 AND allocation_status IN ('reserved','consumed')`,
		pieceInternalID).Scan(&liveSlabs); err != nil {
		return fmt.Errorf("count live slab allocations: %w", err)
	}
	if liveSlabs > 0 {
		return ErrPieceHasSlabs
	}

	if _, err := tx.Exec(ctx, `
		UPDATE fabrication_job_item SET item_deleted_at = NOW() WHERE fabrication_job_item_id = $1`,
		pieceInternalID); err != nil {
		return fmt.Errorf("soft delete piece: %w", err)
	}
	// Steps carry no soft-delete column of their own — a removed piece's rows
	// are hard-deleted so they stop appearing on the checklist.
	if _, err := tx.Exec(ctx, `
		DELETE FROM fabrication_job_step WHERE fabrication_job_item_id = $1`, pieceInternalID); err != nil {
		return fmt.Errorf("delete piece steps: %w", err)
	}

	writeHistory(ctx, tx, st.internalID, "piece_remove", nil, nil, actorEmployeeID)
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit remove piece: %w", err)
	}
	return nil
}

// resolvePieceSalesOrderItem resolves an optional sales-order-line link,
// scoped to the job's own sales order — a piece can't link to a line on a
// different order (shared by AddPiece and UpdatePiece).
func resolvePieceSalesOrderItem(ctx context.Context, tx pgx.Tx, soInternalID int, salesOrderItemUUID string) (any, error) {
	if strings.TrimSpace(salesOrderItemUUID) == "" {
		return nil, nil
	}
	var id int
	err := tx.QueryRow(ctx, `
		SELECT soi.sales_order_item_id FROM sales_order_item soi
		WHERE soi.sales_order_item_uuid = $1 AND soi.sales_order_id = $2 AND soi.item_deleted_at IS NULL`,
		salesOrderItemUUID, soInternalID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "That piece references a line that is not on this job's sales order."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve piece sales order item: %w", err)
	}
	return id, nil
}

// seedPieceSteps inserts the piece-grain canonical steps for one newly added
// piece — the job-grain steps and this piece's steps for a job created with
// pieces already exist (seeded once at Create); this covers the piece
// added afterward.
func seedPieceSteps(ctx context.Context, tx pgx.Tx, jobInternalID, pieceInternalID int) error {
	for _, s := range canonicalSteps {
		if s.Grain != "piece" {
			continue
		}
		if err := insertStep(ctx, tx, jobInternalID, &pieceInternalID, s); err != nil {
			return err
		}
	}
	return nil
}

// getPiece loads one live piece by uuid for a single-piece API response.
func getPiece(ctx context.Context, pool *pgxpool.Pool, pieceUUID string) (*JobItem, error) {
	var p JobItem
	err := pool.QueryRow(ctx, `
		SELECT fi.piece_number, fi.piece_name, fi.piece_type,
		       fi.piece_length_mm, fi.piece_width_mm, fi.piece_thickness_mm,
		       fi.sink_cutout_count, fi.cooktop_cutout_count, fi.seam_count, fi.piece_status,
		       COALESCE(soi.sales_order_item_uuid::text, '')
		FROM fabrication_job_item fi
		LEFT JOIN sales_order_item soi ON soi.sales_order_item_id = fi.sales_order_item_id
		WHERE fi.fabrication_job_item_uuid = $1 AND fi.item_deleted_at IS NULL`, pieceUUID).Scan(
		&p.PieceNumber, &p.PieceName, &p.PieceType,
		&p.LengthMM, &p.WidthMM, &p.ThicknessMM,
		&p.SinkCutoutCount, &p.CooktopCutoutCount, &p.SeamCount, &p.Status,
		&p.SalesOrderItemUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get piece: %w", err)
	}
	p.ID = pieceUUID
	return &p, nil
}
