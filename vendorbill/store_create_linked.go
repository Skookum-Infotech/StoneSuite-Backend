// vendorbill/store_create_linked.go — a bill created WITH purchase order lineage:
// when Create is given a purchaseOrderUuid and at least one line carries a
// purchaseOrderItemId, it records a conversion exactly like the convert path
// does, but at the BILL's quantities. Split from store_create.go.
package vendorbill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// linkedPlan is the validated outcome of a linked create: the purchase order
// lines (for the snapshot) and the quantity each one's conversion line claims.
type linkedPlan struct {
	poID       int
	source     []poSourceLine
	quantities map[int]float64
}

// loadPurchaseOrderStatus returns a live purchase order's status code.
func loadPurchaseOrderStatus(ctx context.Context, tx pgx.Tx, poInternalID int) (string, error) {
	var code string
	err := tx.QueryRow(ctx, `
		SELECT rs.record_status_code
		FROM purchase_order po JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_id = $1 AND po.purchase_order_deleted_at IS NULL`, poInternalID).Scan(&code)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", UnknownPurchaseOrderError()
	}
	if err != nil {
		return "", fmt.Errorf("load purchase order status: %w", err)
	}
	return code, nil
}

// planLinkedCreate validates a new bill's line links against its purchase order
// and stamps each linked line's purchaseOrderItemID. It returns nil when no line
// carries a link (the order stays a plain reference). Every linked line must
// belong to the order, the order must be in a billable status, and the quantities
// may not over-bill what is received and not yet billed elsewhere. It locks the
// order's lines, so concurrent bills for the same goods serialize.
func planLinkedCreate(ctx context.Context, tx pgx.Tx, poID *int, lines []resolvedLine) (*linkedPlan, error) {
	if !anyLinked(lines) {
		return nil, nil
	}
	if poID == nil {
		return nil, ClientError{Msg: "Lines can only link to purchase order lines when a purchase order is selected."}
	}
	status, err := loadPurchaseOrderStatus(ctx, tx, *poID)
	if err != nil {
		return nil, err
	}
	if !IsConvertibleStatus(status) {
		return nil, ClientError{Msg: "Lines can only link to a purchase order that has received items."}
	}
	source, err := loadPurchaseOrderSourceLines(ctx, tx, *poID)
	if err != nil {
		return nil, err
	}
	byUUID := make(map[string]poSourceLine, len(source))
	for _, s := range source {
		byUUID[s.uuid] = s
	}
	linked := make([]linkedQty, 0, len(lines))
	for i := range lines {
		u := lines[i].purchaseOrderItemUUID
		if u == "" {
			continue
		}
		s, ok := byUUID[u]
		if !ok {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: purchaseOrderItemId does not belong to the selected purchase order.", lines[i].lineNumber)}
		}
		id := s.internalID
		lines[i].purchaseOrderItemID = &id
		linked = append(linked, linkedQty{lineNumber: lines[i].lineNumber, poItemID: id, quantity: lines[i].quantity})
	}
	quantities, err := planLinkedQuantities(source, map[int]float64{}, linked)
	if err != nil {
		return nil, err
	}
	return &linkedPlan{poID: *poID, source: source, quantities: quantities}, nil
}

// recordLinkedConversion writes the vendor_bill_conversion row, one
// vendor_bill_conversion_line per claimed purchase order line, and the order's
// own 'convert' history entry -- the same lineage the convert path records.
func recordLinkedConversion(ctx context.Context, tx pgx.Tx, vbInternalID int, vbUUID string, plan *linkedPlan, actorEmployeeID int) error {
	snapshot, err := linkedSnapshot(ctx, tx, vbInternalID, plan)
	if err != nil {
		return err
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("marshal conversion snapshot: %w", err)
	}
	var conversionID int
	if err := tx.QueryRow(ctx, `
		INSERT INTO vendor_bill_conversion (purchase_order_id, vendor_bill_id, converted_by, snapshot)
		VALUES ($1, $2, $3, $4)
		RETURNING vendor_bill_conversion_id`,
		plan.poID, vbInternalID, nullableInt(actorEmployeeID), snapshotJSON).Scan(&conversionID); err != nil {
		return fmt.Errorf("insert vendor_bill_conversion: %w", err)
	}
	for _, s := range plan.source {
		qty, ok := plan.quantities[s.internalID]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO vendor_bill_conversion_line (vendor_bill_conversion_id, purchase_order_item_id, quantity)
			VALUES ($1, $2, $3)`, conversionID, s.internalID, qty); err != nil {
			return fmt.Errorf("insert vendor_bill_conversion_line: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO purchase_order_history (purchase_order_id, action, actor_employee_id, snapshot)
		VALUES ($1, 'convert', $2, jsonb_build_object('vendorBillId', $3::int, 'vendorBillUuid', $4::text))`,
		plan.poID, nullableInt(actorEmployeeID), vbInternalID, vbUUID); err != nil {
		return fmt.Errorf("insert purchase order convert history: %w", err)
	}
	return nil
}

// linkedSnapshot maps each claimed purchase order line uuid to the uuid of the
// (first) bill line linked to it, the same shape the convert path stores.
func linkedSnapshot(ctx context.Context, tx pgx.Tx, vbInternalID int, plan *linkedPlan) (map[string]string, error) {
	poUUIDByID := make(map[int]string, len(plan.source))
	for _, s := range plan.source {
		poUUIDByID[s.internalID] = s.uuid
	}
	rows, err := tx.Query(ctx, `
		SELECT purchase_order_item_id, vendor_bill_item_uuid::text FROM vendor_bill_item
		WHERE vendor_bill_id = $1 AND purchase_order_item_id IS NOT NULL
		ORDER BY line_number`, vbInternalID)
	if err != nil {
		return nil, fmt.Errorf("load linked bill items: %w", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var poItemID int
		var billItemUUID string
		if err := rows.Scan(&poItemID, &billItemUUID); err != nil {
			return nil, fmt.Errorf("scan linked bill item: %w", err)
		}
		if u, ok := poUUIDByID[poItemID]; ok {
			if _, seen := out[u]; !seen {
				out[u] = billItemUUID
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read linked bill items: %w", err)
	}
	return out, nil
}
