// purchaseorder/store_get.go — the shared header SELECT + scan and Get.
// Split from store.go to respect the 300-line file cap (invoice's
// store_line_resolve.go split precedent).
package purchaseorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// poSelect is the base SELECT shared by Get and Search. Column order must
// match scanPurchaseOrder's Scan(...) arg order exactly. Table alias `po`
// matches resolver.go's field expressions.
const poSelect = `
	SELECT po.purchase_order_uuid, COALESCE(po.purchase_order_number,''),
	       rs.record_status_name, rs.record_status_code,
	       po.purchase_order_approval_status,
	       v.vendor_uuid, po.purchase_order_vendor_name, COALESCE(v.vendor_number,''),
	       COALESCE(ou.id::text,''),
	       to_char(po.purchase_order_date,'YYYY-MM-DD'),
	       COALESCE(to_char(po.purchase_order_expected_date,'YYYY-MM-DD'),''),
	       po.purchase_order_reference_number, po.purchase_order_memo,
	       po.purchase_order_notes, po.purchase_order_internal_notes, po.purchase_order_terms_conditions,
	       po.purchase_order_payment_terms, po.purchase_order_currency,
	       po.purchase_order_owner_id, po.purchase_order_sales_tax_percent,
	       po.purchase_order_ship_name, po.purchase_order_ship_attention,
	       po.purchase_order_ship_addr_line1, po.purchase_order_ship_addr_line2, po.purchase_order_ship_addr_suitenum,
	       po.purchase_order_ship_addr_city, po.purchase_order_ship_addr_state, po.purchase_order_ship_addr_zip,
	       po.purchase_order_ship_addr_country, po.purchase_order_ship_phone, po.purchase_order_ship_fax, po.purchase_order_ship_email,
	       po.purchase_order_custom_fields,
	       po.purchase_order_subtotal, po.purchase_order_discount_total, po.purchase_order_tax_total,
	       po.purchase_order_shipping_charge, po.purchase_order_adjustment, po.purchase_order_grand_total,
	       po.purchase_order_created_at, po.purchase_order_updated_at,
	       po.purchase_order_status, po.purchase_order_vendor_id
	FROM purchase_order po
	JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
	JOIN vendor v ON v.vendor_id = po.purchase_order_vendor_id
	LEFT JOIN employee oe ON oe.employee_id = po.purchase_order_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

// poMeta carries the internal numeric ids a purchase order row has but the API
// response deliberately does not expose. Search needs them to mint a keyset
// cursor for sorts that run on those columns (`status`, `vendor_id`) — without
// this the cursor would be built from the wrong field and every page after the
// first would be wrong. Mirrors invoice.invoiceMeta, which solved this first.
type poMeta struct {
	statusID int
	vendorID int
}

func scanPurchaseOrder(row pgx.Row) (*PurchaseOrder, poMeta, error) {
	var p PurchaseOrder
	var meta poMeta
	var customRaw []byte
	if err := row.Scan(
		&p.ID, &p.Number, &p.Status, &p.StatusCode, &p.ApprovalStatus,
		&p.Vendor.ID, &p.Vendor.Name, &p.Vendor.Number, &p.OwnerUserID,
		&p.OrderDate, &p.ExpectedDate,
		&p.ReferenceNumber, &p.Memo,
		&p.Notes, &p.InternalNotes, &p.TermsConditions,
		&p.PaymentTermsID, &p.CurrencyID,
		&p.OwnerEmployeeID, &p.SalesTaxPercent,
		&p.ShipTo.Name, &p.ShipTo.Attention,
		&p.ShipTo.AddrLine1, &p.ShipTo.AddrLine2, &p.ShipTo.SuiteUnit,
		&p.ShipTo.City, &p.ShipTo.StateID, &p.ShipTo.Zip,
		&p.ShipTo.CountryID, &p.ShipTo.Phone, &p.ShipTo.Fax, &p.ShipTo.Email,
		&customRaw,
		&p.Subtotal, &p.DiscountTotal, &p.TaxTotal,
		&p.ShippingCharge, &p.Adjustment, &p.GrandTotal,
		&p.CreatedAt, &p.UpdatedAt,
		&meta.statusID, &meta.vendorID,
	); err != nil {
		return nil, poMeta{}, err
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &p.CustomFields)
	}
	return &p, meta, nil
}

// itemSelect is the base SELECT for a purchase order's live lines. Column
// order must match scanLine's Scan(...) arg order exactly.
const itemSelect = `
	SELECT poi.purchase_order_item_uuid, poi.line_number,
	       ii.inventory_item_uuid,
	       poi.sku, poi.item_name, poi.description, COALESCE(poi.unit_code,''),
	       poi.quantity, poi.qty_received, poi.unit_price, poi.discount_percent, poi.tax_percent,
	       poi.line_subtotal, poi.line_discount, poi.line_tax, poi.line_total
	FROM purchase_order_item poi
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = poi.inventory_item_id
	WHERE poi.purchase_order_id = $1 AND poi.item_deleted_at IS NULL
	ORDER BY poi.line_number`

func scanLine(row pgx.Rows) (Line, error) {
	var l Line
	err := row.Scan(
		&l.ID, &l.LineNumber, &l.InventoryItemID,
		&l.SKU, &l.ItemName, &l.Description, &l.UnitCode,
		&l.Quantity, &l.QtyReceived, &l.UnitPrice, &l.DiscountPercent, &l.TaxPercent,
		&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal,
	)
	return l, err
}

// loadLines fetches a purchase order's live lines by its external uuid.
func loadLines(ctx context.Context, q workflow.Querier, uuid string) ([]Line, error) {
	var internalID int
	if err := q.QueryRow(ctx,
		`SELECT purchase_order_id FROM purchase_order WHERE purchase_order_uuid = $1`, uuid).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve purchase order id: %w", err)
	}
	rows, err := q.Query(ctx, itemSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load purchase order items: %w", err)
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

// Get loads a single live purchase order by its external uuid, including its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*PurchaseOrder, error) {
	p, _, err := scanPurchaseOrder(pool.QueryRow(ctx, poSelect+`
		WHERE po.purchase_order_uuid = $1 AND po.purchase_order_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get purchase order: %w", err)
	}
	items, err := loadLines(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	p.Items = items
	return p, nil
}
