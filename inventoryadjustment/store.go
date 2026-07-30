package inventoryadjustment

// store.go — shared helpers, the error vocabulary and the single-row read.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a uuid matches nothing live. Controllers map it
// to 404 — also for a scope denial, so ids cannot be enumerated.
var ErrNotFound = errors.New("inventory adjustment not found")

// ErrNotEditable is returned when a caller edits a document that has left
// draft.
var ErrNotEditable = errors.New("this adjustment can no longer be edited")

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

func isUniqueViolation(err error) bool { return sqlState(err, "23505") }
func isCheckViolation(err error) bool  { return sqlState(err, "23514") }
func isFKViolation(err error) bool     { return sqlState(err, "23503") }

// isInvalidTextRepresentation reports a malformed uuid (22P02). Treated as
// not-found rather than a 500: a client sending garbage in a path segment gets
// the same answer as one guessing a real-looking id.
func isInvalidTextRepresentation(err error) bool { return sqlState(err, "22P02") }

// pgxQuerier is the read surface shared by a pool and a transaction.
type pgxQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// nullableInt converts a non-positive id to SQL NULL.
//
// NOT for *_deleted_by — use actorOrSystem, since chk_iadj_soft_delete pairs
// that column with its timestamp and NULL there is a 500, not a lost name.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// systemEmployeeID is the seeded fallback actor, matching chartofaccounts and
// inventory. resolveEmployeeID returns 0 whenever the caller's
// employee_user_id has never been populated, which is most callers.
const systemEmployeeID = 1

// actorOrSystem returns the actor, or the system employee when unresolved.
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID <= 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

func nullableIntPtr(v *int) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
}

// orToday returns the given 'YYYY-MM-DD' date, or today when blank.
func orToday(d string) any {
	if d == "" {
		return nil
	}
	return d
}

// FormatNumber renders the document number from the row's serial PK.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", recordTypeCode, serialID)
}

// headerSelect is the canonical header projection.
const headerSelect = `
	SELECT a.inventory_adjustment_uuid, COALESCE(a.adjustment_number,''),
	       a.adjustment_status, rs.record_status_code, rs.record_status_name,
	       a.warehouse_id, w.warehouse_name,
	       to_char(a.adjustment_date, 'YYYY-MM-DD'),
	       a.inventory_reason_id, COALESCE(r.inventory_reason_name,''),
	       a.adjustment_notes, a.adjustment_internal_notes,
	       a.adjustment_owner_id,
	       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
	                            COALESCE(e.employee_last_name,'')), ''), ''),
	       to_char(a.adjustment_posted_at,    'YYYY-MM-DD"T"HH24:MI:SS'),
	       to_char(a.adjustment_cancelled_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
	       a.adjustment_cancel_reason,
	       (SELECT COALESCE(SUM(l.qty_delta),0) FROM inventory_adjustment_line l
	         WHERE l.inventory_adjustment_id = a.inventory_adjustment_id
	           AND l.line_deleted_at IS NULL),
	       a.adjustment_created_at, a.adjustment_updated_at
	FROM inventory_adjustment a
	JOIN lkp_record_status rs        ON rs.record_status_id = a.adjustment_status
	JOIN lkp_warehouse w             ON w.warehouse_id = a.warehouse_id
	LEFT JOIN lkp_inventory_reason r ON r.inventory_reason_id = a.inventory_reason_id
	LEFT JOIN employee e             ON e.employee_id = a.adjustment_owner_id`

func scanHeader(row pgx.Row) (*Adjustment, error) {
	var a Adjustment
	if err := row.Scan(&a.ID, &a.Number,
		&a.StatusID, &a.StatusCode, &a.StatusName,
		&a.WarehouseID, &a.WarehouseName, &a.Date,
		&a.ReasonID, &a.ReasonName, &a.Notes, &a.InternalNotes,
		&a.OwnerID, &a.OwnerName,
		&a.PostedAt, &a.CancelledAt, &a.CancelReason,
		&a.NetDelta, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	return &a, nil
}

// Get loads one adjustment with its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Adjustment, error) {
	a, err := scanHeader(pool.QueryRow(ctx, headerSelect+`
		WHERE a.inventory_adjustment_uuid = $1 AND a.adjustment_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get inventory adjustment: %w", err)
	}
	lines, err := linesFor(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	a.Lines = lines
	return a, nil
}

// linesFor loads a document's live lines, ordered as entered.
func linesFor(ctx context.Context, q pgxQuerier, uuid string) ([]Line, error) {
	rows, err := q.Query(ctx, `
		SELECT l.inventory_adjustment_line_uuid, l.line_number,
		       ii.inventory_item_uuid, l.item_name, l.sku,
		       s.inventory_slab_uuid, l.slab_serial,
		       l.inventory_reason_id, COALESCE(r.inventory_reason_name,''),
		       l.unit_code, l.qty_delta, l.line_notes
		FROM inventory_adjustment_line l
		JOIN inventory_adjustment a      ON a.inventory_adjustment_id = l.inventory_adjustment_id
		JOIN inventory_item ii           ON ii.inventory_item_id = l.inventory_item_id
		LEFT JOIN inventory_slab s       ON s.inventory_slab_id = l.inventory_slab_id
		LEFT JOIN lkp_inventory_reason r ON r.inventory_reason_id = l.inventory_reason_id
		WHERE a.inventory_adjustment_uuid = $1 AND l.line_deleted_at IS NULL
		ORDER BY l.line_number`, uuid)
	if err != nil {
		return nil, fmt.Errorf("load adjustment lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.LineNumber,
			&l.InventoryItemID, &l.InventoryItemName, &l.SKU,
			&l.InventoryUnitID, &l.UnitSerial,
			&l.ReasonID, &l.ReasonName,
			&l.UnitCode, &l.QtyDelta, &l.Notes); err != nil {
			return nil, fmt.Errorf("scan adjustment line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load adjustment lines: %w", err)
	}
	return out, nil
}

// lockHeader loads and locks a document by uuid, returning its internal id and
// current status.
func lockHeader(ctx context.Context, tx pgx.Tx, uuid string) (id, statusID int, statusCode string, err error) {
	err = tx.QueryRow(ctx, `
		SELECT a.inventory_adjustment_id, a.adjustment_status, rs.record_status_code
		FROM inventory_adjustment a
		JOIN lkp_record_status rs ON rs.record_status_id = a.adjustment_status
		WHERE a.inventory_adjustment_uuid = $1 AND a.adjustment_deleted_at IS NULL
		FOR UPDATE OF a`, uuid).Scan(&id, &statusID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, "", ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return 0, 0, "", ErrNotFound
		}
		return 0, 0, "", fmt.Errorf("lock inventory adjustment: %w", err)
	}
	return id, statusID, statusCode, nil
}

// writeHistory appends one row to inventory_adjustment_history.
//
// Takes a pgx.Tx rather than an interface because history must never land in a
// different transaction from the change it describes — a rolled-back edit that
// left an audit row behind would be worse than no audit row.
func writeHistory(ctx context.Context, tx pgx.Tx, id int, action string, from, to *int, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_adjustment_history (
			inventory_adjustment_id, from_status_id, to_status_id, action, actor_employee_id
		) VALUES ($1,$2,$3,$4,$5)`,
		id, from, to, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("write adjustment history: %w", err)
	}
	return nil
}

// mapWriteErr turns a constraint violation into a client-facing 400 where one
// is warranted.
func mapWriteErr(err error, verb string) error {
	switch {
	case isUniqueViolation(err):
		return ClientError{Msg: "This unit is already on a line of this adjustment."}
	case isFKViolation(err):
		return ClientError{Msg: "Unknown item, unit, warehouse or reason code."}
	case isCheckViolation(err):
		return ClientError{Msg: "An adjustment line needs a reason and a non-zero quantity."}
	}
	return fmt.Errorf("%s inventory adjustment: %w", verb, err)
}
