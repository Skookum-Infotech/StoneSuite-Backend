package reports

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrReportNotFound is returned for a report that does not exist, is deleted,
// or is another user's private report — the caller cannot tell which.
var ErrReportNotFound = errors.New("custom report not found")

// SavedReport is a stored Definition plus who owns it and who can see it.
type SavedReport struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Definition Definition `json:"definition"`
	Shared     bool       `json:"shared"`
	OwnerName  string     `json:"ownerName"`
	IsMine     bool       `json:"isMine"`
	UpdatedAt  time.Time  `json:"updatedAt"`
}

const savedReportSelect = `
	SELECT r.custom_report_uuid::text, r.custom_report_name, r.custom_report_definition,
	       r.custom_report_shared,
	       COALESCE(NULLIF(TRIM(e.employee_first_name || ' ' || e.employee_last_name), ''), ''),
	       r.custom_report_owner_id = $1, r.custom_report_updated_at
	FROM custom_report r
	LEFT JOIN employee e ON e.employee_id = r.custom_report_owner_id
	WHERE r.custom_report_deleted_at IS NULL AND (r.custom_report_owner_id = $1 OR r.custom_report_shared)`

func scanSavedReport(row pgx.Row) (SavedReport, error) {
	var r SavedReport
	var def []byte
	if err := row.Scan(&r.ID, &r.Name, &def, &r.Shared, &r.OwnerName, &r.IsMine, &r.UpdatedAt); err != nil {
		return SavedReport{}, err
	}
	if err := json.Unmarshal(def, &r.Definition); err != nil {
		return SavedReport{}, fmt.Errorf("decode custom report %s: %w", r.ID, err)
	}
	return r, nil
}

// ListReports returns the caller's own reports first, then everyone's shared
// ones, newest edit first within each.
func ListReports(ctx context.Context, pool *pgxpool.Pool, employeeID int) ([]SavedReport, error) {
	rows, err := pool.Query(ctx, savedReportSelect+`
		ORDER BY (r.custom_report_owner_id = $1) DESC, r.custom_report_updated_at DESC`, employeeID)
	if err != nil {
		return nil, fmt.Errorf("list custom reports: %w", err)
	}
	defer rows.Close()
	out := []SavedReport{}
	for rows.Next() {
		r, err := scanSavedReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetReport loads one report the caller may see (their own, or shared).
func GetReport(ctx context.Context, pool *pgxpool.Pool, employeeID int, id string) (SavedReport, error) {
	if !IsUUID(id) {
		return SavedReport{}, ErrReportNotFound
	}
	r, err := scanSavedReport(pool.QueryRow(ctx, savedReportSelect+` AND r.custom_report_uuid = $2::uuid`, employeeID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return SavedReport{}, ErrReportNotFound
	}
	if err != nil {
		return SavedReport{}, fmt.Errorf("get custom report: %w", err)
	}
	return r, nil
}

// CreateReport stores a new report owned by employeeID.
func CreateReport(ctx context.Context, pool *pgxpool.Pool, employeeID int, name string, def Definition, shared bool) (SavedReport, error) {
	raw, err := json.Marshal(def)
	if err != nil {
		return SavedReport{}, fmt.Errorf("encode custom report: %w", err)
	}
	var id string
	err = pool.QueryRow(ctx, `
		INSERT INTO custom_report (custom_report_name, custom_report_definition, custom_report_shared, custom_report_owner_id)
		VALUES ($1, $2, $3, $4)
		RETURNING custom_report_uuid::text`, name, raw, shared, employeeID).Scan(&id)
	if err != nil {
		return SavedReport{}, fmt.Errorf("create custom report: %w", err)
	}
	return GetReport(ctx, pool, employeeID, id)
}

// UpdateReport rewrites a report. Only its owner may; anyone else gets
// ErrReportNotFound.
func UpdateReport(ctx context.Context, pool *pgxpool.Pool, employeeID int, id, name string, def Definition, shared bool) (SavedReport, error) {
	if !IsUUID(id) {
		return SavedReport{}, ErrReportNotFound
	}
	raw, err := json.Marshal(def)
	if err != nil {
		return SavedReport{}, fmt.Errorf("encode custom report: %w", err)
	}
	tag, err := pool.Exec(ctx, `
		UPDATE custom_report
		SET custom_report_name = $3, custom_report_definition = $4, custom_report_shared = $5,
		    custom_report_updated_at = CURRENT_TIMESTAMP
		WHERE custom_report_uuid = $1::uuid AND custom_report_owner_id = $2 AND custom_report_deleted_at IS NULL`,
		id, employeeID, name, raw, shared)
	if err != nil {
		return SavedReport{}, fmt.Errorf("update custom report: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return SavedReport{}, ErrReportNotFound
	}
	return GetReport(ctx, pool, employeeID, id)
}

// DeleteReport soft-deletes a report. Only its owner may.
func DeleteReport(ctx context.Context, pool *pgxpool.Pool, employeeID int, id string) error {
	if !IsUUID(id) {
		return ErrReportNotFound
	}
	tag, err := pool.Exec(ctx, `
		UPDATE custom_report SET custom_report_deleted_at = CURRENT_TIMESTAMP
		WHERE custom_report_uuid = $1::uuid AND custom_report_owner_id = $2 AND custom_report_deleted_at IS NULL`,
		id, employeeID)
	if err != nil {
		return fmt.Errorf("delete custom report: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrReportNotFound
	}
	return nil
}
