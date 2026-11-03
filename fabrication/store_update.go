package fabrication

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Update patches a job's header fields (scheduling, assignment, site, notes,
// custom fields). Pieces and status are managed by their own endpoints, so this
// is header-only. Returns the refreshed job.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateJobInput, actorEmployeeID int) (*Job, error) {
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	ct, err := pool.Exec(ctx, `
		UPDATE fabrication_job SET
			job_site_customer_name = $2, job_site_addr_line1 = $3, job_site_addr_line2 = $4,
			job_site_addr_city = $5, job_site_addr_state = $6, job_site_addr_zip = $7, job_site_phone = $8,
			job_template_date = $9::date, job_fabrication_start = $10::date, job_promised_install_date = $11::date,
			job_owner_id = $12, job_templater_id = $13, job_fabricator_id = $14, job_install_crew_id = $15,
			job_notes = $16, job_custom_fields = $17,
			fabrication_job_updated_at = NOW(), fabrication_job_updated_by = $18,
			fabrication_job_record_version = fabrication_job_record_version + 1
		WHERE fabrication_job_uuid = $1 AND fabrication_job_deleted_at IS NULL`,
		uuid,
		in.SiteCustomerName, in.SiteAddrLine1, in.SiteAddrLine2,
		in.SiteCity, in.SiteStateID, in.SiteZip, in.SitePhone,
		nullableDate(in.TemplateDate), nullableDate(in.FabricationStart), nullableDate(in.PromisedInstallDate),
		nullableIntPtr(in.OwnerEmployeeID), nullableIntPtr(in.TemplaterEmployeeID),
		nullableIntPtr(in.FabricatorEmployeeID), nullableIntPtr(in.InstallCrewEmployeeID),
		in.Notes, custom, nullableInt(actorEmployeeID))
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (state, or an employee) does not exist."}
		}
		return nil, fmt.Errorf("update fabrication job: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return Get(ctx, pool, uuid)
}

// ErrJobHasDependents is returned when a delete would orphan effects it cannot
// reverse: live material allocations or a live shortage purchase order.
var ErrJobHasDependents = errors.New("job has live material allocations or purchase orders; release or remove them before deleting")

// SoftDelete marks a job deleted. Draft/cancelled jobs only, and only when no
// live dependent rows remain whose effects a delete would not reverse (slabs
// still reserved or consumed without a disposition, open cutting runs, live
// shortage purchase orders). The job row is locked FOR UPDATE so the gate and
// the delete serialize against transitions and allocation.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete fabrication job: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	st, err := lockJob(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if st.statusCode != StatusDraft && st.statusCode != StatusCancelled {
		return ClientError{Msg: "Only draft or cancelled jobs can be deleted; cancel the job first to release its material."}
	}
	var dependents int
	if err := tx.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM fabrication_job_slab
			  WHERE fabrication_job_id = $1
			    AND (allocation_status = 'reserved'
			         OR (allocation_status = 'consumed' AND disposition IS NULL)))
		  + (SELECT COUNT(*) FROM fabrication_cutting_run
			  WHERE fabrication_job_id = $1 AND state = 'started')
		  + (SELECT COUNT(*) FROM fabrication_shortage_purchase sp
			  JOIN purchase_order po ON po.purchase_order_id = sp.purchase_order_id
			  WHERE sp.fabrication_job_id = $1 AND po.purchase_order_deleted_at IS NULL)`,
		st.internalID).Scan(&dependents); err != nil {
		return fmt.Errorf("count fabrication job dependents: %w", err)
	}
	if dependents > 0 {
		return ErrJobHasDependents
	}
	ct, err := tx.Exec(ctx, `
		UPDATE fabrication_job SET fabrication_job_deleted_at = NOW(), fabrication_job_deleted_by = $2
		WHERE fabrication_job_id = $1 AND fabrication_job_deleted_at IS NULL`, st.internalID, actorOrSystem(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("soft delete fabrication job: %w", err)
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete fabrication job: %w", err)
	}
	return nil
}
