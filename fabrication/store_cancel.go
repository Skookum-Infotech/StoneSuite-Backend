package fabrication

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrDispositionRequired is returned when a job cannot be cancelled because it
// has consumed slabs whose disposition has not been declared (spec §4.4.1).
// Maps to HTTP 409.
var ErrDispositionRequired = errors.New("every cut slab needs a disposition before this job can be cancelled")

// Cancel cancels a job. Uncut (reserved) slabs are released. If the job has
// consumed slabs, each must first have a disposition declared via
// RecordDisposition — otherwise the cancel is rejected 409 and the job is
// marked cancel-requested so disposition becomes legal (§4.4).
func Cancel(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*Job, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin cancel: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if IsTerminal(st.statusCode) {
		return nil, ErrInvalidTransition
	}

	// Any consumed slab still awaiting a disposition blocks the cancel.
	var undeclared int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM fabrication_job_slab
		WHERE fabrication_job_id = $1 AND allocation_status = 'consumed' AND disposition IS NULL`,
		st.internalID).Scan(&undeclared); err != nil {
		return nil, fmt.Errorf("count undeclared dispositions: %w", err)
	}
	if undeclared > 0 {
		// Mark cancel-requested so RecordDisposition is now legal (§4.4.3).
		if _, err := tx.Exec(ctx, `
			UPDATE fabrication_job SET job_cancel_requested_at = COALESCE(job_cancel_requested_at, NOW())
			WHERE fabrication_job_id = $1`, st.internalID); err != nil {
			return nil, fmt.Errorf("mark cancel requested: %w", err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit cancel request: %w", err)
		}
		return nil, ErrDispositionRequired
	}

	// No undeclared consumed slabs: release any reserved ones and cancel.
	if err := releaseReservedSlabs(ctx, tx, st.internalID); err != nil {
		return nil, err
	}
	recordTypeID, err := recordTypeIDByCode(ctx, tx, fjobRecordTypeCode)
	if err != nil {
		return nil, err
	}
	cancID, err := statusIDByCode(ctx, tx, recordTypeID, StatusCancelled)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE fabrication_job SET
			fabrication_job_status = $2, job_cancel_requested_at = NULL, job_held_from_status_id = NULL,
			fabrication_job_updated_at = NOW(), fabrication_job_updated_by = $3,
			fabrication_job_record_version = fabrication_job_record_version + 1
		WHERE fabrication_job_id = $1`, st.internalID, cancID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("cancel fabrication job: %w", err)
	}
	writeHistory(ctx, tx, st.internalID, "cancel", &st.statusID, &cancID, actorEmployeeID)
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit cancel: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// DispositionInput declares what happened to one consumed slab on a cancelling
// job (spec §4.4.1).
type DispositionInput struct {
	SlabUUID      string  `json:"slabUuid"`
	Disposition   string  `json:"disposition"` // recovered | scrapped | delivered
	RecoveredArea float64 `json:"recoveredArea"`
	LengthMM      float64 `json:"lengthMm"`
	WidthMM       float64 `json:"widthMm"`
	ThicknessMM   float64 `json:"thicknessMm"`
}

// RecordDisposition declares the fate of one consumed slab on a job that is being
// cancelled. Write-once per slab (idempotent via disposition IS NULL). A
// 'recovered' disposition mints a child offcut and adds its area back to stock,
// capped at the parent's REMAINING area (§4.4.1, §4.5).
func RecordDisposition(ctx context.Context, pool *pgxpool.Pool, jobUUID string, in DispositionInput, actorEmployeeID int) error {
	if in.Disposition != "recovered" && in.Disposition != "scrapped" && in.Disposition != "delivered" {
		return ClientError{Msg: "Disposition must be recovered, scrapped, or delivered."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin disposition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The job must be cancel-requested for disposition to be legal (§4.4.3).
	var jobID int
	var cancelRequested bool
	err = tx.QueryRow(ctx, `
		SELECT fabrication_job_id, (job_cancel_requested_at IS NOT NULL)
		FROM fabrication_job WHERE fabrication_job_uuid = $1 AND fabrication_job_deleted_at IS NULL
		FOR UPDATE`, jobUUID).Scan(&jobID, &cancelRequested)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load job for disposition: %w", err)
	}
	if !cancelRequested {
		return ClientError{Msg: "Disposition can only be recorded while a job is being cancelled."}
	}

	// Lock the consumed allocation + parent slab.
	var allocID, slabID, itemID, whID int
	var parentArea, consumedFrom float64
	err = tx.QueryRow(ctx, `
		SELECT fjs.fabrication_job_slab_id, s.inventory_slab_id, s.inventory_item_id, s.warehouse_id, s.slab_area,
		       COALESCE((SELECT SUM(c.slab_area) FROM inventory_slab c WHERE c.slab_parent_slab_id = s.inventory_slab_id AND c.slab_deleted_at IS NULL), 0)
		FROM fabrication_job_slab fjs
		JOIN inventory_slab s ON s.inventory_slab_id = fjs.inventory_slab_id
		WHERE fjs.fabrication_job_id = $1 AND s.inventory_slab_uuid = $2
		  AND fjs.allocation_status = 'consumed' AND fjs.disposition IS NULL
		FOR UPDATE OF fjs, s`, jobID, in.SlabUUID).Scan(&allocID, &slabID, &itemID, &whID, &parentArea, &consumedFrom)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "No consumed, undeclared slab of that id on this job."}
	}
	if err != nil {
		return fmt.Errorf("lock consumed slab: %w", err)
	}

	var recoveredSlabID any
	var recoveredArea any
	if in.Disposition == "recovered" {
		remaining := parentArea - consumedFrom
		if in.RecoveredArea <= 0 || in.RecoveredArea > remaining {
			return ClientError{Msg: fmt.Sprintf("Recovered area must be between 0 and the remaining %.3f.", remaining)}
		}
		childID, err := mintOffcut(ctx, tx, slabID, in, actorEmployeeID)
		if err != nil {
			return err
		}
		// The offcut is new stock (§2.9): a 'recovered' ledger row carries the delta.
		if err := ledgerAndStock(ctx, tx, childID, itemID, whID, ledgerRecovered, in.RecoveredArea, &allocID, actorEmployeeID); err != nil {
			return err
		}
		recoveredSlabID = childID
		recoveredArea = in.RecoveredArea
	}

	ct, err := tx.Exec(ctx, `
		UPDATE fabrication_job_slab SET
			disposition = $2, disposition_recorded_at = NOW(), disposition_recorded_by = $3,
			recovered_slab_id = $4, recovered_area = $5
		WHERE fabrication_job_slab_id = $1 AND disposition IS NULL`,
		allocID, in.Disposition, nullableInt(actorEmployeeID), recoveredSlabID, recoveredArea)
	if err != nil {
		return fmt.Errorf("record disposition: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ClientError{Msg: "That slab's disposition was already recorded."}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit disposition: %w", err)
	}
	return nil
}

// mintOffcut inserts a child slab (form='cut', available) from a consumed parent,
// inheriting material, vendor, and supplier code, with serial {parent}-R{n}.
func mintOffcut(ctx context.Context, tx pgx.Tx, parentSlabID int, in DispositionInput, actorEmployeeID int) (int, error) {
	// Next free -R suffix for this parent, computed while the parent is locked.
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_slab WHERE slab_parent_slab_id = $1`, parentSlabID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count offcuts: %w", err)
	}
	var childID int
	err := tx.QueryRow(ctx, `
		INSERT INTO inventory_slab (
			slab_serial, slab_vendor_id, slab_supplier_code, inventory_item_id, warehouse_id,
			slab_bundle_id, slab_block_id, slab_lot,
			slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area, slab_area_unit_id,
			slab_form, slab_parent_slab_id, slab_status, slab_grade, slab_finish, slab_created_by)
		SELECT p.slab_serial || '-R' || ($2::int + 1), p.slab_vendor_id, p.slab_supplier_code,
		       p.inventory_item_id, p.warehouse_id, p.slab_bundle_id, p.slab_block_id, p.slab_lot,
		       $3, $4, $5, $6, p.slab_area_unit_id,
		       'cut', p.inventory_slab_id, 'available', p.slab_grade, p.slab_finish, $7
		FROM inventory_slab p WHERE p.inventory_slab_id = $1
		RETURNING inventory_slab_id`,
		parentSlabID, n,
		defaultPositive(in.LengthMM), defaultPositive(in.WidthMM), defaultPositive(in.ThicknessMM),
		in.RecoveredArea, nullableInt(actorEmployeeID)).Scan(&childID)
	if err != nil {
		if isCheckViolation(err) {
			return 0, ClientError{Msg: "Offcut dimensions or area are out of range."}
		}
		return 0, fmt.Errorf("mint offcut: %w", err)
	}
	return childID, nil
}

// defaultPositive returns v when positive, else 1 (schema requires dims > 0; a
// caller that omits offcut dimensions gets a nominal placeholder).
func defaultPositive(v float64) float64 {
	if v > 0 {
		return v
	}
	return 1
}
