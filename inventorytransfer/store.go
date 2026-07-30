package inventorytransfer

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
var ErrNotFound = errors.New("inventory transfer not found")

// ErrNotEditable is returned when a caller edits a transfer that has left draft.
var ErrNotEditable = errors.New("this transfer can no longer be edited")

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

func nullableStr(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

// systemEmployeeID is the seeded fallback actor for the paired *_by columns
// that a CHECK forbids from being NULL. See inventory.actorOrSystem.
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
	SELECT t.inventory_transfer_uuid, COALESCE(t.transfer_number,''),
	       t.transfer_status, rs.record_status_code, rs.record_status_name,
	       t.from_warehouse_id, fw.warehouse_name,
	       t.to_warehouse_id,   tw.warehouse_name,
	       b.inventory_bin_uuid, COALESCE(b.bin_path,''),
	       to_char(t.transfer_date, 'YYYY-MM-DD'),
	       to_char(t.transfer_expected_date, 'YYYY-MM-DD'),
	       t.transfer_carrier, t.transfer_tracking_number,
	       t.transfer_notes, t.transfer_internal_notes, t.transfer_owner_id,
	       to_char(t.transfer_shipped_at,   'YYYY-MM-DD"T"HH24:MI:SS'),
	       to_char(t.transfer_received_at,  'YYYY-MM-DD"T"HH24:MI:SS'),
	       to_char(t.transfer_cancelled_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
	       t.transfer_cancel_reason,
	       (SELECT COALESCE(SUM(l.qty),0) FROM inventory_transfer_line l
	         WHERE l.inventory_transfer_id = t.inventory_transfer_id
	           AND l.line_deleted_at IS NULL),
	       t.transfer_created_at, t.transfer_updated_at
	FROM inventory_transfer t
	JOIN lkp_record_status rs  ON rs.record_status_id = t.transfer_status
	JOIN lkp_warehouse fw      ON fw.warehouse_id = t.from_warehouse_id
	JOIN lkp_warehouse tw      ON tw.warehouse_id = t.to_warehouse_id
	LEFT JOIN inventory_bin b  ON b.inventory_bin_id = t.to_bin_id`

func scanHeader(row pgx.Row) (*Transfer, error) {
	var t Transfer
	if err := row.Scan(&t.ID, &t.Number,
		&t.StatusID, &t.StatusCode, &t.StatusName,
		&t.FromWarehouseID, &t.FromWarehouseName,
		&t.ToWarehouseID, &t.ToWarehouseName,
		&t.ToBinID, &t.ToBinPath,
		&t.Date, &t.ExpectedDate, &t.Carrier, &t.TrackingNumber,
		&t.Notes, &t.InternalNotes, &t.OwnerID,
		&t.ShippedAt, &t.ReceivedAt, &t.CancelledAt, &t.CancelReason,
		&t.TotalQty, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// Get loads one transfer with its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Transfer, error) {
	t, err := scanHeader(pool.QueryRow(ctx, headerSelect+`
		WHERE t.inventory_transfer_uuid = $1 AND t.transfer_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get inventory transfer: %w", err)
	}
	lines, err := linesFor(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	t.Lines = lines
	return t, nil
}

func linesFor(ctx context.Context, q pgxQuerier, uuid string) ([]Line, error) {
	rows, err := q.Query(ctx, `
		SELECT l.inventory_transfer_line_uuid, l.line_number,
		       ii.inventory_item_uuid, l.item_name, l.sku,
		       s.inventory_slab_uuid, l.slab_serial,
		       l.unit_code, l.qty, l.line_notes
		FROM inventory_transfer_line l
		JOIN inventory_transfer t  ON t.inventory_transfer_id = l.inventory_transfer_id
		JOIN inventory_item ii     ON ii.inventory_item_id = l.inventory_item_id
		LEFT JOIN inventory_slab s ON s.inventory_slab_id = l.inventory_slab_id
		WHERE t.inventory_transfer_uuid = $1 AND l.line_deleted_at IS NULL
		ORDER BY l.line_number`, uuid)
	if err != nil {
		return nil, fmt.Errorf("load transfer lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.LineNumber,
			&l.InventoryItemID, &l.InventoryItemName, &l.SKU,
			&l.InventoryUnitID, &l.UnitSerial,
			&l.UnitCode, &l.Qty, &l.Notes); err != nil {
			return nil, fmt.Errorf("scan transfer line: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// transferRow is the header state a mutation needs, read under lock.
type transferRow struct {
	id         int
	statusID   int
	statusCode string
	fromWHID   int
	toWHID     int
	toBinID    *int
}

func lockHeader(ctx context.Context, tx pgx.Tx, uuid string) (transferRow, error) {
	var r transferRow
	err := tx.QueryRow(ctx, `
		SELECT t.inventory_transfer_id, t.transfer_status, rs.record_status_code,
		       t.from_warehouse_id, t.to_warehouse_id, t.to_bin_id
		FROM inventory_transfer t
		JOIN lkp_record_status rs ON rs.record_status_id = t.transfer_status
		WHERE t.inventory_transfer_uuid = $1 AND t.transfer_deleted_at IS NULL
		FOR UPDATE OF t`, uuid).Scan(
		&r.id, &r.statusID, &r.statusCode, &r.fromWHID, &r.toWHID, &r.toBinID)
	if errors.Is(err, pgx.ErrNoRows) {
		return transferRow{}, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return transferRow{}, ErrNotFound
		}
		return transferRow{}, fmt.Errorf("lock inventory transfer: %w", err)
	}
	return r, nil
}

func writeHistory(ctx context.Context, tx pgx.Tx, id int, action string, from, to *int, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_transfer_history (
			inventory_transfer_id, from_status_id, to_status_id, action, actor_employee_id
		) VALUES ($1,$2,$3,$4,$5)`,
		id, from, to, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("write transfer history: %w", err)
	}
	return nil
}

func mapWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		return ClientError{Msg: "This unit is already on a line of this transfer."}
	case isFKViolation(err):
		return ClientError{Msg: "Unknown item, unit, warehouse or bin."}
	case isCheckViolation(err):
		return ClientError{Msg: "A transfer needs two different warehouses and positive line quantities."}
	}
	return fmt.Errorf("%s inventory transfer: %w", verb, err)
}
