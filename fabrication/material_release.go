package fabrication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/authz"
)

// ReleaseMaterialInput identifies a still-reserved physical unit.
type ReleaseMaterialInput struct {
	CommandMeta
	SlabID string `json:"slabId"`
}

// ReleaseMaterial frees a reservation while preserving its reviewed layout history.
func ReleaseMaterial(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in ReleaseMaterialInput) (ActionResult, error) {
	actor.permission = authz.ActionUpdate
	return releaseMaterial(ctx, pool, jobID, actor, in)
}

func releaseMaterial(ctx context.Context, pool *pgxpool.Pool, jobID string, actor ActionActor, in ReleaseMaterialInput) (ActionResult, error) {
	payload, err := json.Marshal(in)
	if err != nil {
		return ActionResult{}, fmt.Errorf("encode material release: %w", err)
	}
	return executeAction(ctx, pool, jobID, actor, in.CommandMeta, actionCommand{Code: "release_material", SubjectID: in.SlabID, Payload: payload, MovementPermission: actor.permission != ""}, func(ctx context.Context, tx pgx.Tx, state actionState) error {
		st, err := lockJob(ctx, tx, jobID)
		if err != nil {
			return err
		}
		if IsTerminal(st.statusCode) {
			return ClientError{Msg: "Material cannot be released from a completed or cancelled job."}
		}
		var slabID int
		var status string
		err = tx.QueryRow(ctx, `SELECT inventory_slab_id,slab_status FROM inventory_slab WHERE inventory_slab_uuid=$1 AND slab_deleted_at IS NULL FOR UPDATE`, in.SlabID).Scan(&slabID, &status)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock released slab: %w", err)
		}
		if err = requireUnstartedMaterial(ctx, tx, state.JobID, slabID); err != nil {
			return err
		}
		if status != "reserved" {
			return ClientError{Msg: "Only reserved material can be released."}
		}
		tag, err := tx.Exec(ctx, `UPDATE fabrication_job_slab SET allocation_status='released' WHERE fabrication_job_id=$1 AND inventory_slab_id=$2 AND allocation_status='reserved'`, state.JobID, slabID)
		if err != nil {
			return fmt.Errorf("release material allocation: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrNotFound
		}
		if _, err = tx.Exec(ctx, `UPDATE inventory_slab SET slab_status='available',slab_updated_at=NOW() WHERE inventory_slab_id=$1`, slabID); err != nil {
			return fmt.Errorf("free released slab: %w", err)
		}
		return nil
	})
}
