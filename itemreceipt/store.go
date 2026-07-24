// itemreceipt/store.go — shared helpers for the Item Receipt relational store
// (see the package doc in types.go).
package itemreceipt

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when an item receipt uuid matches nothing live.
var ErrNotFound = errors.New("item receipt not found")

// ErrPONotReceivable is returned when the source purchase order is not in a
// status that can accept goods (only SENT and PART can).
var ErrPONotReceivable = errors.New("purchase order is not open for receiving")

// ErrAlreadyPosted is returned when a caller tries to edit, delete or re-post
// a receipt whose quantities have already moved (AD-5).
var ErrAlreadyPosted = errors.New("item receipt has already been posted")

// ErrMovementAlreadyApplied is returned when the inventory ledger's per-line
// partial unique indexes reject a duplicate movement — a receipt line posted or
// reversed twice. It should be unreachable through the status guards; it exists
// because the schema, not the guards, is what makes double-counting impossible.
var ErrMovementAlreadyApplied = errors.New("this inventory movement has already been applied")

// ErrOverReceipt is returned when a posting exceeds the over-receipt tolerance
// and the caller does not hold the item_receipt:approve override (AD-3).
// Controllers map it to 403, not 400: the request is well-formed, the caller
// just is not allowed to wave it through.
var ErrOverReceipt = errors.New("delivery exceeds the ordered quantity beyond the accepted tolerance")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400, mirroring purchaseorder.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// irctRecordTypeCode is the lkp_record_type code for Item Receipt.
const irctRecordTypeCode = "IRCT"

// Status codes, seeded for record_type 14 and adopted verbatim (AD-1).
const (
	pendingStatusCode  = "PEND"
	partialStatusCode  = "PART"
	receivedStatusCode = "RCVD"
	voidStatusCode     = "VOID"
)

// receivableStatusCodes are the purchase order statuses that can accept goods:
// the order has been issued to the vendor, and is not closed or cancelled.
var receivableStatusCodes = map[string]bool{"SENT": true, "PART": true}

// ledgerEventReceived / ledgerEventReturned are the inventory_ledger event
// codes this module writes (posting and its reversal).
const (
	ledgerEventReceived = "received"
	ledgerEventReturned = "returned"
)

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// orNow returns the given "yyyy-mm-dd" date string, or today when blank.
func orNow(d string) string {
	if d == "" {
		return "now"
	}
	return d
}

// isForeignKeyViolation reports whether err is a PostgreSQL FK-constraint
// violation (code 23503) — an invalid caller-supplied reference id.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (code 23505). The inventory ledger's partial unique indexes turn a
// double-post into this, rather than into double-counted stock.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// isCheckViolation reports whether err is a PostgreSQL CHECK-constraint
// violation (code 23514) — chiefly inventory_stock's quantity_on_hand >= 0,
// which a reversal can hit when the goods have already been consumed.
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// recordTypeIDByCode resolves a lkp_record_type code to its internal id.
func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

// statusIDByCode resolves a lkp_record_status code (scoped to a record type)
// to its internal id.
func statusIDByCode(ctx context.Context, q workflow.Querier, recordTypeID int, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("status %q: %w", code, err)
	}
	return id, nil
}

// defaultWarehouseID resolves the tenant's default warehouse, used when the
// caller does not name one (AD-4). lkp_warehouse has a partial unique index on
// warehouse_is_default, so at most one row can match.
func defaultWarehouseID(ctx context.Context, q workflow.Querier) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT warehouse_id FROM lkp_warehouse
		WHERE warehouse_is_default AND warehouse_deleted_at IS NULL`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ClientError{Msg: "No default warehouse is configured; specify warehouseId explicitly."}
	}
	if err != nil {
		return 0, fmt.Errorf("resolve default warehouse: %w", err)
	}
	return id, nil
}

// sourceOrder is what a receipt needs from its purchase order at create time.
type sourceOrder struct {
	internalID int
	statusCode string
	vendorID   int
	vendorName string
}

// resolveSourceOrder loads the purchase order a receipt is being created
// against, and enforces that it is open for receiving. The vendor is taken
// from here and never from the caller, so a receipt cannot name a different
// counterparty than the order it settles.
func resolveSourceOrder(ctx context.Context, q workflow.Querier, poUUID string) (*sourceOrder, error) {
	var s sourceOrder
	err := q.QueryRow(ctx, `
		SELECT po.purchase_order_id, rs.record_status_code,
		       po.purchase_order_vendor_id, po.purchase_order_vendor_name
		FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_uuid = $1 AND po.purchase_order_deleted_at IS NULL`, poUUID,
	).Scan(&s.internalID, &s.statusCode, &s.vendorID, &s.vendorName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown purchase order."}
	}
	if err != nil {
		return nil, fmt.Errorf("load source purchase order: %w", err)
	}
	if !receivableStatusCodes[s.statusCode] {
		return nil, ErrPONotReceivable
	}
	return &s, nil
}

// poLineSnapshot is what a receipt line needs from the ordered line it settles.
type poLineSnapshot struct {
	internalID      int
	inventoryItemID *int
	sku, name, desc string
	unitID          *int
	unitCode        string
	quantity        float64
	qtyReceived     float64
}

// resolvePOLine loads an ordered line's snapshot fields by its external uuid,
// scoped to the receipt's own purchase order — a caller cannot splice a line
// from someone else's order into this receipt.
func resolvePOLine(ctx context.Context, q workflow.Querier, poInternalID int, uuid string) (*poLineSnapshot, error) {
	var s poLineSnapshot
	err := q.QueryRow(ctx, `
		SELECT poi.purchase_order_item_id, poi.inventory_item_id,
		       poi.sku, poi.item_name, poi.description, poi.unit_id, COALESCE(poi.unit_code,''),
		       poi.quantity, poi.qty_received
		FROM purchase_order_item poi
		WHERE poi.purchase_order_item_uuid = $1
		  AND poi.purchase_order_id = $2
		  AND poi.item_deleted_at IS NULL`, uuid, poInternalID).Scan(
		&s.internalID, &s.inventoryItemID,
		&s.sku, &s.name, &s.desc, &s.unitID, &s.unitCode,
		&s.quantity, &s.qtyReceived)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown purchase order line for this order: " + uuid}
	}
	if err != nil {
		return nil, fmt.Errorf("load purchase order line: %w", err)
	}
	return &s, nil
}

// validateCustom validates custom fields against the "item_receipt" workflow's
// field definitions (≤15, typed) — the corrected skeleton, mirroring
// purchaseorder.validateCustom.
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "item_receipt")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load item_receipt workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load item_receipt field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}

// writeHistory records one item_receipt_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, irInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO item_receipt_history (item_receipt_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		irInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}
