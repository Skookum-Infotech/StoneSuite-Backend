// Package fabrication implements the relational Fabrication & Installation
// module: a production job (fabrication_job) spawned from a sales order, with
// fabricated pieces (fabrication_job_item), serialized slab allocations
// (fabrication_job_slab over inventory_slab), a 16-step checklist
// (fabrication_job_step), a status trail (fabrication_job_history), and dual
// approval gates. Stock for slab-tracked items is ledger-derived: every change
// to inventory_stock.quantity_on_hand is one inventory_slab_ledger row in the
// same transaction, so the two can never drift (spec §2.9).
package fabrication

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a job uuid matches nothing live.
var ErrNotFound = errors.New("fabrication job not found")

// ClientError signals a client-caused failure a controller maps to 400/409.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// isForeignKeyViolation reports a PostgreSQL FK-constraint violation (23503).
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isUniqueViolation reports a PostgreSQL unique-constraint violation (23505) —
// e.g. losing the double-sell race on uq_fab_slab_live (spec §4.3).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isCheckViolation reports a PostgreSQL CHECK-constraint violation (23514).
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

const fjobRecordTypeCode = "FJOB"

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ----- header select + scan -------------------------------------------------

const jobSelect = `
	SELECT fj.fabrication_job_uuid, COALESCE(fj.fabrication_job_number,''),
	       rs.record_status_name, rs.record_status_code,
	       fj.job_approval_status,
	       so.sales_order_uuid,
	       c.customer_uuid, c.customer_name,
	       COALESCE(ou.id::text,''),
	       COALESCE(hs.record_status_code,''),
	       (fj.job_cancel_requested_at IS NOT NULL),
	       fj.job_site_customer_name, fj.job_site_addr_line1, fj.job_site_addr_line2,
	       fj.job_site_addr_city, fj.job_site_addr_state, fj.job_site_addr_zip, fj.job_site_phone,
	       COALESCE(to_char(fj.job_template_date,'YYYY-MM-DD'),''),
	       COALESCE(to_char(fj.job_fabrication_start,'YYYY-MM-DD'),''),
	       COALESCE(to_char(fj.job_promised_install_date,'YYYY-MM-DD'),''),
	       COALESCE(to_char(fj.job_actual_install_date,'YYYY-MM-DD'),''),
	       fj.job_owner_id, fj.job_templater_id, fj.job_fabricator_id, fj.job_install_crew_id,
	       fj.job_notes, fj.job_custom_fields,
	       fj.fabrication_job_created_at, fj.fabrication_job_updated_at
	FROM fabrication_job fj
	JOIN lkp_record_status rs ON rs.record_status_id = fj.fabrication_job_status
	JOIN sales_order so ON so.sales_order_id = fj.sales_order_id
	JOIN customer c ON c.customer_id = fj.fabrication_job_customer_id
	LEFT JOIN lkp_record_status hs ON hs.record_status_id = fj.job_held_from_status_id
	LEFT JOIN employee oe ON oe.employee_id = fj.job_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	var customRaw []byte
	if err := row.Scan(
		&j.ID, &j.Number, &j.Status, &j.StatusCode, &j.ApprovalStatus,
		&j.SalesOrderID, &j.Customer.ID, &j.Customer.Name, &j.OwnerUserID,
		&j.HeldFromStatusCode, &j.CancelRequested,
		&j.Site.CustomerName, &j.Site.AddrLine1, &j.Site.AddrLine2,
		&j.Site.City, &j.Site.StateID, &j.Site.Zip, &j.Site.Phone,
		&j.TemplateDate, &j.FabricationStart, &j.PromisedInstallDate, &j.ActualInstallDate,
		&j.OwnerEmployeeID, &j.TemplaterEmployeeID, &j.FabricatorEmployeeID, &j.InstallCrewEmployeeID,
		&j.Notes, &customRaw,
		&j.CreatedAt, &j.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &j.CustomFields)
	}
	return &j, nil
}

// Get loads a single live job by uuid, including pieces and steps.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Job, error) {
	j, err := scanJob(pool.QueryRow(ctx, jobSelect+`
		WHERE fj.fabrication_job_uuid = $1 AND fj.fabrication_job_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get fabrication job: %w", err)
	}
	pieces, err := loadPieces(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	steps, err := loadSteps(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	j.Pieces = pieces
	j.Steps = steps
	return j, nil
}

func loadPieces(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]JobItem, error) {
	rows, err := pool.Query(ctx, `
		SELECT fi.fabrication_job_item_uuid, fi.piece_number, fi.piece_name, fi.piece_type,
		       fi.piece_length_mm, fi.piece_width_mm, fi.piece_thickness_mm,
		       fi.sink_cutout_count, fi.cooktop_cutout_count, fi.seam_count, fi.piece_status,
		       COALESCE(soi.sales_order_item_uuid::text, '')
		FROM fabrication_job_item fi
		JOIN fabrication_job fj ON fj.fabrication_job_id = fi.fabrication_job_id
		LEFT JOIN sales_order_item soi ON soi.sales_order_item_id = fi.sales_order_item_id
		WHERE fj.fabrication_job_uuid = $1 AND fi.item_deleted_at IS NULL
		ORDER BY fi.piece_number`, uuid)
	if err != nil {
		return nil, fmt.Errorf("load pieces: %w", err)
	}
	defer rows.Close()
	out := []JobItem{}
	for rows.Next() {
		var p JobItem
		if err := rows.Scan(&p.ID, &p.PieceNumber, &p.PieceName, &p.PieceType,
			&p.LengthMM, &p.WidthMM, &p.ThicknessMM,
			&p.SinkCutoutCount, &p.CooktopCutoutCount, &p.SeamCount, &p.Status,
			&p.SalesOrderItemUUID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func loadSteps(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]Step, error) {
	rows, err := pool.Query(ctx, `
		SELECT fs.step_code, fs.step_sequence, fs.step_status, fs.step_notes, fs.step_payload,
		       fs.step_started_at, fs.step_completed_at
		FROM fabrication_job_step fs
		JOIN fabrication_job fj ON fj.fabrication_job_id = fs.fabrication_job_id
		WHERE fj.fabrication_job_uuid = $1
		ORDER BY fs.step_sequence, fs.fabrication_job_step_id`, uuid)
	if err != nil {
		return nil, fmt.Errorf("load steps: %w", err)
	}
	defer rows.Close()
	out := []Step{}
	for rows.Next() {
		var s Step
		var payloadRaw []byte
		if err := rows.Scan(&s.Code, &s.Sequence, &s.Status, &s.Notes, &payloadRaw,
			&s.StartedAt, &s.CompletedAt); err != nil {
			return nil, err
		}
		if len(payloadRaw) > 0 {
			_ = json.Unmarshal(payloadRaw, &s.Payload)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ----- lookups --------------------------------------------------------------

func recordTypeIDByCode(ctx context.Context, q querier, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

func statusIDByCode(ctx context.Context, q querier, recordTypeID int, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("status %q: %w", code, err)
	}
	return id, nil
}

// employeeIDByIdentity resolves a control-plane identity to a tenant employee_id.
func employeeIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (int, bool) {
	if identityID == "" {
		return 0, false
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT e.employee_id FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE u.identity_id = $1 AND e.employee_deleted_at IS NULL`, identityID).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// nullableIntPtr passes through a *int as a nullable arg.
func nullableIntPtr(v *int) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
}

// nullableDate returns a "yyyy-mm-dd" string as a nullable date arg.
func nullableDate(d string) any {
	if d == "" {
		return nil
	}
	return d
}

// writeHistory appends a fabrication_job_history row (best-effort inside tx).
func writeHistory(ctx context.Context, tx pgx.Tx, jobInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO fabrication_job_history (fabrication_job_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		jobInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}
