// itemreceipt/store_create.go
package itemreceipt

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/idempotency"
)

// Create inserts a new item receipt (header + lines) inside one transaction:
// resolves the source purchase order and inherits its vendor snapshot, resolves
// every line against that order's ordered lines, defaults the warehouse, assigns
// the IRCT number, and starts the receipt at PEND.
//
// Nothing is posted here — no stock moves and no qty_received changes until
// Post is called. Creating a receipt is recording paperwork; posting it is the
// act that touches inventory. (The receiving UI no longer stops here: it calls
// CreateAndPost. A PEND receipt now exists only for API callers of this
// function and for receipts created before that change.)
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateItemReceiptInput, actorEmployeeID int) (*ItemReceipt, error) {
	return createReceipt(ctx, pool, in, nil, actorEmployeeID)
}

// CreateAndPost creates a receipt and posts it in one transaction, so the
// receipt is never left Pending: if posting is refused (over-receipt without
// the override, order no longer receivable, ...) nothing is saved at all.
// canApproveOverReceipt and in.OverReceiptReason carry the same meaning as on
// Post.
func CreateAndPost(
	ctx context.Context, pool *pgxpool.Pool, in CreateItemReceiptInput,
	canApproveOverReceipt bool, actorEmployeeID int,
) (*ItemReceipt, error) {
	return createReceipt(ctx, pool, in, &postOptions{canApproveOverReceipt: canApproveOverReceipt}, actorEmployeeID)
}

// postOptions, when non-nil, makes createReceipt post the receipt it just
// inserted before committing.
type postOptions struct{ canApproveOverReceipt bool }

func createReceipt(
	ctx context.Context, pool *pgxpool.Pool, in CreateItemReceiptInput,
	post *postOptions, actorEmployeeID int,
) (*ItemReceipt, error) {
	if strings.TrimSpace(in.PurchaseOrderUUID) == "" {
		return nil, ClientError{Msg: "A purchase order is required."}
	}
	// A retry whose first attempt already committed gets that receipt back
	// before any validation: the order may have moved on since, and a replay
	// must answer like the original, not re-judge it.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Lookup(ctx, pool, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			return Get(ctx, pool, prior)
		}
	}
	if err := validateWriteInput(len(in.Items), in.ReceiptDate); err != nil {
		return nil, err
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create item receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Claim inside the create's transaction: a concurrent duplicate blocks
	// here until this one commits, then replays it instead of creating.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Claim(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			_ = tx.Rollback(ctx)
			return Get(ctx, pool, prior)
		}
	}

	src, err := resolveSourceOrder(ctx, tx, in.PurchaseOrderUUID, post != nil)
	if err != nil {
		return nil, err
	}

	// The warehouse is resolved before the lines: a slab's bin is validated
	// against it.
	warehouseID := 0
	if in.WarehouseID != nil && *in.WarehouseID > 0 {
		warehouseID = *in.WarehouseID
	} else {
		warehouseID, err = defaultWarehouseID(ctx, tx)
		if err != nil {
			return nil, err
		}
	}

	lines, err := resolveLines(ctx, tx, src.internalID, warehouseID, in.Items)
	if err != nil {
		return nil, err
	}
	if err := requireExplicitWarehouse(lines, in.WarehouseID); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, irctRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve IRCT record type: %w", err)
	}
	pendingStatusID, err := statusIDByCode(ctx, tx, recordTypeID, pendingStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve PEND status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO item_receipt (
			record_type, item_receipt_status, purchase_order_id,
			item_receipt_vendor_id, item_receipt_vendor_name, warehouse_id,
			item_receipt_date, item_receipt_packing_slip, item_receipt_carrier,
			item_receipt_tracking_number, item_receipt_bill_of_lading,
			item_receipt_notes, item_receipt_internal_notes,
			item_receipt_owner_id, item_receipt_custom_fields, item_receipt_created_by
		) VALUES ($1,$2,$3, $4,$5,$6, $7::date,$8,$9, $10,$11, $12,$13, $14,$15,$16)
		RETURNING item_receipt_id, item_receipt_uuid`,
		recordTypeID, pendingStatusID, src.internalID,
		src.vendorID, src.vendorName, warehouseID,
		orNow(in.ReceiptDate), in.PackingSlip, in.Carrier,
		in.TrackingNumber, in.BillOfLading,
		in.Notes, in.InternalNotes,
		nullableInt(ownerEmployeeID), custom, nullableInt(actorEmployeeID),
	).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (location or owner) does not exist."}
		}
		return nil, fmt.Errorf("insert item receipt: %w", err)
	}

	if _, err := assignNumber(ctx, tx, int64(internalID)); err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" {
		if err := idempotency.Complete(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, newUUID); err != nil {
			return nil, err
		}
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &pendingStatusID, actorEmployeeID)

	if post != nil {
		if err := postReceipt(ctx, tx, newUUID, PostInput{OverReceiptReason: in.OverReceiptReason},
			post.canApproveOverReceipt, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create item receipt: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
