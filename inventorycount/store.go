package inventorycount

// store.go — shared helpers, the error vocabulary and the single-row read.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a uuid matches nothing live.
var ErrNotFound = errors.New("inventory count not found")

// ErrNotEditable is returned when a caller edits a count that has left draft.
var ErrNotEditable = errors.New("this count can no longer be edited")

// ClientError signals a client-caused failure a controller maps to 400.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

func sqlState(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func isUniqueViolation(err error) bool         { return sqlState(err, "23505") }
func isCheckViolation(err error) bool          { return sqlState(err, "23514") }
func isFKViolation(err error) bool             { return sqlState(err, "23503") }
func isInvalidTextRepresentation(e error) bool { return sqlState(e, "22P02") }

// pgxQuerier is the read surface shared by a pool and a transaction.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullableIntPtr(v *int) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// systemEmployeeID is the seeded fallback for the paired *_by columns a CHECK
// forbids from being NULL. See inventory.actorOrSystem.
const systemEmployeeID = 1

func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID <= 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

// FormatNumber renders the document number from the row's serial PK.
func FormatNumber(serialID int64) string { return fmt.Sprintf("%s-%06d", recordTypeCode, serialID) }

const headerSelect = `
	SELECT c.inventory_count_uuid, COALESCE(c.count_number,''),
	       c.count_status, rs.record_status_code, rs.record_status_name,
	       c.warehouse_id, w.warehouse_name,
	       b.inventory_bin_uuid, COALESCE(b.bin_path,''),
	       to_char(c.count_date, 'YYYY-MM-DD'),
	       to_char(c.count_frozen_at,    'YYYY-MM-DD"T"HH24:MI:SS'),
	       c.count_notes, c.count_internal_notes, c.count_owner_id,
	       to_char(c.count_posted_at,    'YYYY-MM-DD"T"HH24:MI:SS'),
	       to_char(c.count_cancelled_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
	       c.count_cancel_reason,
	       (SELECT COUNT(*) FROM inventory_count_line l
	         WHERE l.inventory_count_id = c.inventory_count_id AND l.line_deleted_at IS NULL),
	       (SELECT COUNT(*) FROM inventory_count_line l
	         WHERE l.inventory_count_id = c.inventory_count_id AND l.line_deleted_at IS NULL
	           AND l.counted_qty IS NOT NULL),
	       (SELECT COUNT(*) FROM inventory_count_line l
	         WHERE l.inventory_count_id = c.inventory_count_id AND l.line_deleted_at IS NULL
	           AND l.count_variance <> 0),
	       (SELECT COALESCE(SUM(l.count_variance),0) FROM inventory_count_line l
	         WHERE l.inventory_count_id = c.inventory_count_id AND l.line_deleted_at IS NULL),
	       c.count_created_at, c.count_updated_at
	FROM inventory_count c
	JOIN lkp_record_status rs ON rs.record_status_id = c.count_status
	JOIN lkp_warehouse w      ON w.warehouse_id = c.warehouse_id
	LEFT JOIN inventory_bin b ON b.inventory_bin_id = c.inventory_bin_id`

func scanHeader(row pgx.Row) (*Count, error) {
	var c Count
	if err := row.Scan(&c.ID, &c.Number,
		&c.StatusID, &c.StatusCode, &c.StatusName,
		&c.WarehouseID, &c.WarehouseName, &c.BinID, &c.BinPath,
		&c.Date, &c.FrozenAt, &c.Notes, &c.InternalNotes, &c.OwnerID,
		&c.PostedAt, &c.CancelledAt, &c.CancelReason,
		&c.LineCount, &c.CountedCount, &c.VarianceCount, &c.NetVariance,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

// Get loads one count with its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Count, error) {
	c, err := scanHeader(pool.QueryRow(ctx, headerSelect+`
		WHERE c.inventory_count_uuid = $1 AND c.count_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get inventory count: %w", err)
	}
	lines, err := linesFor(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	c.Lines = lines
	return c, nil
}

func linesFor(ctx context.Context, q pgxQuerier, uuid string) ([]Line, error) {
	rows, err := q.Query(ctx, `
		SELECT l.inventory_count_line_uuid, l.line_number,
		       ii.inventory_item_uuid, l.item_name, l.sku,
		       s.inventory_slab_uuid, l.slab_serial, COALESCE(b.bin_path,''),
		       l.unit_code, l.inventory_reason_id, COALESCE(r.inventory_reason_name,''),
		       l.system_qty, l.counted_qty, l.count_variance, l.is_unexpected,
		       to_char(l.counted_at, 'YYYY-MM-DD"T"HH24:MI:SS'), l.line_notes
		FROM inventory_count_line l
		JOIN inventory_count c           ON c.inventory_count_id = l.inventory_count_id
		JOIN inventory_item ii           ON ii.inventory_item_id = l.inventory_item_id
		LEFT JOIN inventory_slab s       ON s.inventory_slab_id = l.inventory_slab_id
		LEFT JOIN inventory_bin b        ON b.inventory_bin_id = l.inventory_bin_id
		LEFT JOIN lkp_inventory_reason r ON r.inventory_reason_id = l.inventory_reason_id
		WHERE c.inventory_count_uuid = $1 AND l.line_deleted_at IS NULL
		ORDER BY l.line_number`, uuid)
	if err != nil {
		return nil, fmt.Errorf("load count lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.LineNumber,
			&l.InventoryItemID, &l.InventoryItemName, &l.SKU,
			&l.InventoryUnitID, &l.UnitSerial, &l.BinPath,
			&l.UnitCode, &l.ReasonID, &l.ReasonName,
			&l.SystemQty, &l.CountedQty, &l.Variance, &l.IsUnexpected,
			&l.CountedAt, &l.Notes); err != nil {
			return nil, fmt.Errorf("scan count line: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// countRow is the header state a mutation needs, read under lock.
type countRow struct {
	id          int
	statusID    int
	statusCode  string
	warehouseID int
	binID       *int
	binPath     string
}

func lockHeader(ctx context.Context, tx pgx.Tx, uuid string) (countRow, error) {
	var r countRow
	err := tx.QueryRow(ctx, `
		SELECT c.inventory_count_id, c.count_status, rs.record_status_code,
		       c.warehouse_id, c.inventory_bin_id, COALESCE(b.bin_path,'')
		FROM inventory_count c
		JOIN lkp_record_status rs ON rs.record_status_id = c.count_status
		LEFT JOIN inventory_bin b ON b.inventory_bin_id = c.inventory_bin_id
		WHERE c.inventory_count_uuid = $1 AND c.count_deleted_at IS NULL
		FOR UPDATE OF c`, uuid).Scan(
		&r.id, &r.statusID, &r.statusCode, &r.warehouseID, &r.binID, &r.binPath)
	if errors.Is(err, pgx.ErrNoRows) {
		return countRow{}, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return countRow{}, ErrNotFound
		}
		return countRow{}, fmt.Errorf("lock inventory count: %w", err)
	}
	return r, nil
}

func writeHistory(ctx context.Context, tx pgx.Tx, id int, action string, from, to *int, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_count_history (
			inventory_count_id, from_status_id, to_status_id, action, actor_employee_id
		) VALUES ($1,$2,$3,$4,$5)`,
		id, from, to, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("write count history: %w", err)
	}
	return nil
}

func mapWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		return ClientError{Msg: "That item or unit is already on a line of this count."}
	case isFKViolation(err):
		return ClientError{Msg: "Unknown item, unit, warehouse, bin or reason code."}
	case isCheckViolation(err):
		return ClientError{Msg: "A counted quantity cannot be negative."}
	}
	return fmt.Errorf("%s inventory count: %w", verb, err)
}
