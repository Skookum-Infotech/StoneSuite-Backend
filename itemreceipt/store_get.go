// itemreceipt/store_get.go — the shared header SELECT + scan and Get.
// Split from store.go to respect the 300-line file cap.
package itemreceipt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// irSelect is the base SELECT shared by Get and Search. Column order must
// match scanItemReceipt's Scan(...) arg order exactly. Table alias `ir`
// matches resolver.go's field expressions, and the po/v joins are what let
// resolver.go filter on purchase_order_id / vendor_id.
const irSelect = `
	SELECT ir.item_receipt_uuid, COALESCE(ir.item_receipt_number,''),
	       rs.record_status_name, rs.record_status_code,
	       po.purchase_order_uuid, COALESCE(po.purchase_order_number,''), pors.record_status_code,
	       v.vendor_uuid, ir.item_receipt_vendor_name, COALESCE(v.vendor_number,''),
	       COALESCE(ou.id::text,''),
	       ir.warehouse_id, COALESCE(w.warehouse_name,''),
	       to_char(ir.item_receipt_date,'YYYY-MM-DD'),
	       ir.item_receipt_packing_slip, ir.item_receipt_carrier,
	       ir.item_receipt_tracking_number, ir.item_receipt_bill_of_lading,
	       ir.item_receipt_notes, ir.item_receipt_internal_notes,
	       ir.item_receipt_owner_id,
	       ir.item_receipt_posted_at, ir.item_receipt_voided_at,
	       ir.item_receipt_void_reason, ir.item_receipt_over_receipt_reason,
	       ir.item_receipt_custom_fields,
	       ir.item_receipt_created_at, ir.item_receipt_updated_at
	FROM item_receipt ir
	JOIN lkp_record_status rs ON rs.record_status_id = ir.item_receipt_status
	JOIN purchase_order po ON po.purchase_order_id = ir.purchase_order_id
	JOIN lkp_record_status pors ON pors.record_status_id = po.purchase_order_status
	JOIN vendor v ON v.vendor_id = ir.item_receipt_vendor_id
	LEFT JOIN lkp_warehouse w ON w.warehouse_id = ir.warehouse_id
	LEFT JOIN employee oe ON oe.employee_id = ir.item_receipt_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

func scanItemReceipt(row pgx.Row) (*ItemReceipt, error) {
	var r ItemReceipt
	var customRaw []byte
	if err := row.Scan(
		&r.ID, &r.Number, &r.Status, &r.StatusCode,
		&r.PurchaseOrder.ID, &r.PurchaseOrder.Number, &r.PurchaseOrder.StatusCode,
		&r.Vendor.ID, &r.Vendor.Name, &r.Vendor.Number, &r.OwnerUserID,
		&r.WarehouseID, &r.WarehouseName,
		&r.ReceiptDate,
		&r.PackingSlip, &r.Carrier,
		&r.TrackingNumber, &r.BillOfLading,
		&r.Notes, &r.InternalNotes,
		&r.OwnerEmployeeID,
		&r.PostedAt, &r.VoidedAt,
		&r.VoidReason, &r.OverReceiptReason,
		&customRaw,
		&r.CreatedAt, &r.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &r.CustomFields)
	}
	return &r, nil
}

// lineSelect is the base SELECT for a receipt's live lines. It joins the
// ordered line so the response can show progress (ordered vs. received-to-date)
// alongside this receipt's own quantities. Column order must match scanLine.
const lineSelect = `
	SELECT irl.item_receipt_line_uuid, irl.line_number,
	       poi.purchase_order_item_uuid,
	       ii.inventory_item_uuid,
	       irl.sku, irl.item_name, irl.description, COALESCE(irl.unit_code,''),
	       irl.qty_received, irl.qty_rejected,
	       poi.quantity, poi.qty_received,
	       irl.line_notes
	FROM item_receipt_line irl
	JOIN purchase_order_item poi ON poi.purchase_order_item_id = irl.purchase_order_item_id
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = irl.inventory_item_id
	WHERE irl.item_receipt_id = $1 AND irl.item_deleted_at IS NULL
	ORDER BY irl.line_number`

func scanLine(row pgx.Rows) (Line, error) {
	var l Line
	err := row.Scan(
		&l.ID, &l.LineNumber,
		&l.PurchaseOrderItemID,
		&l.InventoryItemID,
		&l.SKU, &l.ItemName, &l.Description, &l.UnitCode,
		&l.QtyReceived, &l.QtyRejected,
		&l.QtyOrdered, &l.QtyReceivedToDate,
		&l.LineNotes,
	)
	return l, err
}

// loadLines fetches a receipt's live lines by its external uuid.
func loadLines(ctx context.Context, q workflow.Querier, uuid string) ([]Line, error) {
	var internalID int
	if err := q.QueryRow(ctx,
		`SELECT item_receipt_id FROM item_receipt WHERE item_receipt_uuid = $1`, uuid).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve item receipt id: %w", err)
	}
	rows, err := q.Query(ctx, lineSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load item receipt lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get loads a single live item receipt by its external uuid, including its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*ItemReceipt, error) {
	r, err := scanItemReceipt(pool.QueryRow(ctx, irSelect+`
		WHERE ir.item_receipt_uuid = $1 AND ir.item_receipt_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get item receipt: %w", err)
	}
	items, err := loadLines(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	r.Items = items
	return r, nil
}
