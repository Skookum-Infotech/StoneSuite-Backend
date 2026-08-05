// requisition/store_get.go — the shared header SELECT + scan and Get. Split
// from store.go to respect the 300-line file cap (invoice's
// store_line_resolve.go / purchaseorder's store_get.go split precedent).
package requisition

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// reqnSelect is the base SELECT shared by Get and Search. Column order must
// match scanRequisition's Scan(...) arg order exactly. Table alias `reqn`
// matches resolver.go's field expressions. The vendor join is LEFT (AD-2:
// vendor is nullable); requestor is INNER (requisition_requested_by_id is
// NOT NULL and doubles as the IDOR scope owner — AD-5). The conversion join
// is LEFT so an unconverted requisition still returns a row.
const reqnSelect = `
	SELECT reqn.requisition_uuid, COALESCE(reqn.requisition_number,''),
	       rs.record_status_name, rs.record_status_code,
	       reqn.requisition_approval_status,
	       COALESCE(ru.id::text,''),
	       reqn.requisition_requested_by_id, reqn.requisition_department,
	       COALESCE(to_char(reqn.requisition_needed_by_date,'YYYY-MM-DD'),''),
	       reqn.requisition_priority, reqn.requisition_memo,
	       v.vendor_uuid, reqn.requisition_vendor_name, COALESCE(v.vendor_number,''),
	       reqn.requisition_payment_terms,
	       reqn.requisition_custom_fields,
	       reqn.requisition_sales_tax_percent,
	       reqn.requisition_subtotal, reqn.requisition_tax_total, reqn.requisition_estimated_total,
	       po.purchase_order_uuid,
	       reqn.requisition_created_at, reqn.requisition_updated_at,
	       reqn.requisition_status
	FROM requisition reqn
	JOIN lkp_record_status rs ON rs.record_status_id = reqn.requisition_status
	JOIN employee re ON re.employee_id = reqn.requisition_requested_by_id
	LEFT JOIN users ru ON ru.id = re.employee_user_id
	LEFT JOIN vendor v ON v.vendor_id = reqn.requisition_vendor_id
	LEFT JOIN requisition_conversion rc ON rc.requisition_id = reqn.requisition_id
	LEFT JOIN purchase_order po ON po.purchase_order_id = rc.purchase_order_id`

// reqnMeta carries the internal numeric id a requisition row has but the API
// response deliberately does not expose. Search needs it to mint a keyset
// cursor for the `status` sort, mirrors purchaseorder.poMeta.
type reqnMeta struct {
	statusID int
}

func scanRequisition(row pgx.Row) (*Requisition, reqnMeta, error) {
	var p Requisition
	var meta reqnMeta
	var customRaw []byte
	var vendorUUID *string
	var vendorName, vendorNumber string
	var convertedPOUUID *string
	if err := row.Scan(
		&p.ID, &p.Number, &p.Status, &p.StatusCode, &p.ApprovalStatus,
		&p.OwnerUserID,
		&p.RequestedByEmployeeID, &p.Department,
		&p.NeededByDate,
		&p.Priority, &p.Memo,
		&vendorUUID, &vendorName, &vendorNumber,
		&p.PaymentTermsID,
		&customRaw,
		&p.SalesTaxPercent,
		&p.Subtotal, &p.TaxTotal, &p.EstimatedTotal,
		&convertedPOUUID,
		&p.CreatedAt, &p.UpdatedAt,
		&meta.statusID,
	); err != nil {
		return nil, reqnMeta{}, err
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &p.CustomFields)
	}
	if vendorUUID != nil {
		p.Vendor = &VendorRef{ID: *vendorUUID, Name: vendorName, Number: vendorNumber}
	}
	if convertedPOUUID != nil {
		p.ConvertedPurchaseOrderID = *convertedPOUUID
	}
	return &p, meta, nil
}

// itemSelect is the base SELECT for a requisition's live lines. Column order
// must match scanLine's Scan(...) arg order exactly.
const itemSelect = `
	SELECT ri.requisition_item_uuid, ri.line_number,
	       ii.inventory_item_uuid,
	       ri.sku, ri.item_name, ri.description, COALESCE(ri.unit_code,''),
	       ri.quantity, ri.estimated_unit_price, ri.estimated_amount
	FROM requisition_item ri
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = ri.inventory_item_id
	WHERE ri.requisition_id = $1 AND ri.item_deleted_at IS NULL
	ORDER BY ri.line_number`

func scanLine(row pgx.Rows) (Line, error) {
	var l Line
	err := row.Scan(
		&l.ID, &l.LineNumber, &l.InventoryItemID,
		&l.SKU, &l.ItemName, &l.Description, &l.UnitCode,
		&l.Quantity, &l.EstimatedUnitPrice, &l.EstimatedAmount,
	)
	return l, err
}

// loadLines fetches a requisition's live lines by its external uuid.
func loadLines(ctx context.Context, q workflow.Querier, uuid string) ([]Line, error) {
	var internalID int
	if err := q.QueryRow(ctx,
		`SELECT requisition_id FROM requisition WHERE requisition_uuid = $1`, uuid).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve requisition id: %w", err)
	}
	rows, err := q.Query(ctx, itemSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load requisition items: %w", err)
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

// Get loads a single live requisition by its external uuid, including its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Requisition, error) {
	p, _, err := scanRequisition(pool.QueryRow(ctx, reqnSelect+`
		WHERE reqn.requisition_uuid = $1 AND reqn.requisition_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get requisition: %w", err)
	}
	items, err := loadLines(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	p.Items = items
	return p, nil
}
