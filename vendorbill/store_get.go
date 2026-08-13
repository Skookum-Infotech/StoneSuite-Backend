// vendorbill/store_get.go — the shared header SELECT + scan and Get.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// vbSelect is the base SELECT shared by Get and Search. Column order must
// match scanVendorBill's Scan(...) arg order exactly. Table alias `vb`
// matches resolver.go's field expressions.
const vbSelect = `
	SELECT vb.vendor_bill_uuid, COALESCE(vb.vendor_bill_number,''),
	       rs.record_status_name, rs.record_status_code,
	       vb.vendor_bill_approval_status,
	       v.vendor_uuid, vb.vendor_bill_vendor_name, COALESCE(v.vendor_number,''),
	       COALESCE(po.purchase_order_uuid::text,''), COALESCE(po.purchase_order_number,''),
	       COALESCE(ou.id::text,''), vb.vendor_bill_owner_id,
	       vb.vendor_bill_vendor_invoice_number, vb.vendor_bill_reference_number,
	       to_char(vb.vendor_bill_date,'YYYY-MM-DD'), COALESCE(to_char(vb.vendor_bill_due_date,'YYYY-MM-DD'),''),
	       vb.vendor_bill_payment_terms, vb.vendor_bill_currency, vb.vendor_bill_exchange_rate,
	       vb.vendor_bill_sales_tax_percent,
	       vb.vendor_bill_memo, vb.vendor_bill_notes, vb.vendor_bill_internal_notes, vb.vendor_bill_terms_conditions,
	       vb.vendor_bill_subtotal, vb.vendor_bill_discount_total, vb.vendor_bill_tax_total,
	       vb.vendor_bill_adjustment, vb.vendor_bill_grand_total,
	       vb.vendor_bill_amount_paid, vb.vendor_bill_balance_due,
	       vb.vendor_bill_custom_fields,
	       vb.vendor_bill_created_at, vb.vendor_bill_updated_at, vb.vendor_bill_record_version,
	       vb.vendor_bill_id, vb.vendor_bill_status, vb.vendor_bill_vendor_id
	FROM vendor_bill vb
	JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
	JOIN vendor v ON v.vendor_id = vb.vendor_bill_vendor_id
	LEFT JOIN purchase_order po ON po.purchase_order_id = vb.vendor_bill_purchase_order_id
	LEFT JOIN employee oe ON oe.employee_id = vb.vendor_bill_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

// vbMeta carries the internal numeric ids a vendor bill row has but the API
// response deliberately does not expose. Search needs them to mint a keyset
// cursor for sorts that run on those columns (status, vendor_id).
type vbMeta struct {
	internalID int
	statusID   int
	vendorID   int
}

func scanVendorBill(row pgx.Row) (*VendorBill, vbMeta, error) {
	var (
		b                          VendorBill
		ownerEmpID                 *int
		paymentTermsID, currencyID *int
		custom                     map[string]any
		meta                       vbMeta
		poUUID, poNum              string
	)
	err := row.Scan(
		&b.ID, &b.Number,
		&b.StatusName, &b.StatusCode,
		&b.ApprovalStatus,
		&b.Vendor.ID, &b.Vendor.Name, &b.Vendor.Number,
		&poUUID, &poNum,
		&b.OwnerUserID, &ownerEmpID,
		&b.VendorInvoiceNumber, &b.ReferenceNumber,
		&b.BillDate, &b.DueDate,
		&paymentTermsID, &currencyID, &b.ExchangeRate,
		&b.SalesTaxPercent,
		&b.Memo, &b.Notes, &b.InternalNotes, &b.TermsConditions,
		&b.Subtotal, &b.DiscountTotal, &b.TaxTotal,
		&b.Adjustment, &b.GrandTotal,
		&b.AmountPaid, &b.BalanceDue,
		&custom, &b.CreatedAt, &b.UpdatedAt, &b.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.vendorID,
	)
	if err != nil {
		return nil, vbMeta{}, err
	}
	if poUUID != "" {
		b.PurchaseOrder = &PurchaseOrderRef{ID: poUUID, Number: poNum}
	}
	b.OwnerEmployeeID = ownerEmpID
	b.PaymentTermsID = paymentTermsID
	b.CurrencyID = currencyID
	if custom == nil {
		custom = map[string]any{}
	}
	b.CustomFields = custom
	b.Items = []Line{}
	return &b, meta, nil
}

const lineSelect = `
	SELECT vbi.vendor_bill_item_uuid, vbi.line_number,
	       COALESCE(ii.inventory_item_uuid::text,''), COALESCE(poi.purchase_order_item_uuid::text,''),
	       vbi.sku, vbi.item_name, vbi.description, vbi.unit_id, vbi.unit_code,
	       vbi.quantity, vbi.unit_price, vbi.discount_percent, vbi.tax_rate_id, vbi.tax_percent,
	       vbi.line_subtotal, vbi.line_discount, vbi.line_tax, vbi.line_total
	FROM vendor_bill_item vbi
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = vbi.inventory_item_id
	LEFT JOIN purchase_order_item poi ON poi.purchase_order_item_id = vbi.purchase_order_item_id
	WHERE vbi.vendor_bill_id = $1 AND vbi.item_deleted_at IS NULL
	ORDER BY vbi.line_number ASC`

func loadLines(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Line, error) {
	rows, err := pool.Query(ctx, lineSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load vendor bill lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		var invItemUUID, poItemUUID string
		if err := rows.Scan(&l.ID, &l.LineNumber, &invItemUUID, &poItemUUID,
			&l.SKU, &l.ItemName, &l.Description, &l.UnitID, &l.UnitCode,
			&l.Quantity, &l.UnitPrice, &l.DiscountPercent, &l.TaxRateID, &l.TaxPercent,
			&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal); err != nil {
			return nil, fmt.Errorf("scan vendor bill line: %w", err)
		}
		if invItemUUID != "" {
			l.InventoryItemID = &invItemUUID
		}
		if poItemUUID != "" {
			l.PurchaseOrderItemID = &poItemUUID
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get loads a single live vendor bill (header + lines + payments) by its
// external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*VendorBill, error) {
	b, meta, err := scanVendorBill(pool.QueryRow(ctx, vbSelect+`
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get vendor bill: %w", err)
	}
	lines, err := loadLines(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	b.Items = lines
	payments, err := loadPayments(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	b.Payments = payments
	return b, nil
}
