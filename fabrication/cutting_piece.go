package fabrication

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// The caller holds the job lock. Measurements come only from the approved snapshot.
func createCuttingPiece(ctx context.Context, tx pgx.Tx, jobID, employeeID int, piece MeasuredPiece) (int, error) {
	var id int
	err := tx.QueryRow(ctx, `INSERT INTO fabrication_job_item
 (fabrication_job_id,piece_number,piece_name,piece_length_mm,piece_width_mm,piece_thickness_mm,production_stage,item_created_by)
 SELECT $1,COALESCE(MAX(piece_number),0)+1,$2,$3,$4,$5,$6,$7
 FROM fabrication_job_item WHERE fabrication_job_id=$1 RETURNING fabrication_job_item_id`,
		jobID, piece.Name, piece.LengthMM, piece.WidthMM, piece.ThicknessMM, StageCutting, employeeID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create cutting production piece: %w", err)
	}
	return id, nil
}

func requireUnboundPiece(ctx context.Context, tx pgx.Tx, jobID int, pieceUUID string) error {
	var bound bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM fabrication_cutting_selection s
 JOIN fabrication_job_item p USING(fabrication_job_item_id)
 WHERE p.fabrication_job_id=$1 AND p.fabrication_job_item_uuid=$2)`, jobID, pieceUUID).Scan(&bound)
	if err != nil {
		return fmt.Errorf("check cutting piece binding: %w", err)
	}
	if bound {
		return ErrPiecesLocked
	}
	return nil
}
