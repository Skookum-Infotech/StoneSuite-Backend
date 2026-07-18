// Package salesorder implements the relational Sales Order module: a header
// table (sales_order) with ordered line items (sales_order_item), a status
// trail (sales_order_history), and inventory allocation — a sibling of the
// CRM `customer` table (crmstore/relational_store.go), not the generic v1
// JSONB workflow engine (spec AD-1). Money is snapshotted and stored (never
// recomputed from live prices); billing/shipping/item data is snapshotted at
// create time so later master-data edits don't rewrite history (spec AD-4).
package salesorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a sales order uuid matches nothing live.
var ErrNotFound = errors.New("sales order not found")

// ClientError signals a client-caused failure (validation, bad input, an
// illegal transition) that a controller maps to HTTP 400/409, mirroring
// crmstore.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// isForeignKeyViolation reports whether err is a PostgreSQL FK-constraint
// violation (code 23503) — an invalid caller-supplied reference id (customer,
// payment terms, tax rate, warehouse, state/country, ...).
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isCheckViolation reports whether err is a PostgreSQL CHECK-constraint
// violation (code 23514) — a numeric field landed outside the range a schema
// CHECK enforces (e.g. a percent column) that Go-level validation didn't
// already catch. A safety net so an unmapped constraint surfaces as a 400,
// not an opaque 500.
func isCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23514"
}

// sordRecordTypeCode is the lkp_record_type code for Sales Order (spec §1).
const sordRecordTypeCode = "SORD"

// draftStatusCode is the status every new order starts at (spec §8).
const draftStatusCode = "DRFT"

// ----- header select + scan ---------------------------------------------

// orderSelect returns every field an Update round-trip needs back (spec:
// Edit must be able to reload an order and re-save it without silently
// blanking billing/shipping or any header field the create/update contract
// accepts) — not just the display-only summary fields. Used by both Get and
// Search; the extra columns are still a single-table scan, no added JOIN.
const orderSelect = `
	SELECT so.sales_order_uuid, COALESCE(so.sales_order_number,''),
	       rs.record_status_name, rs.record_status_code,
	       so.sales_order_approval_status,
	       c.customer_uuid, c.customer_name,
	       COALESCE(ou.id::text,''),
	       to_char(so.sales_order_date,'YYYY-MM-DD'),
	       COALESCE(to_char(so.sales_order_expected_delivery,'YYYY-MM-DD'),''),
	       COALESCE(to_char(so.sales_order_payment_due_date,'YYYY-MM-DD'),''),
	       so.sales_order_po_number, so.sales_order_reference_number, so.sales_order_memo,
	       so.sales_order_notes, so.sales_order_internal_notes, so.sales_order_terms_conditions,
	       so.sales_order_payment_terms, so.sales_order_price_level, so.sales_order_currency,
	       so.sales_order_sales_rep_id, so.sales_order_owner_id, so.sales_order_sales_tax_percent,
	       so.sales_order_ship_same_as_bill,
	       so.sales_order_bill_customer_name, so.sales_order_bill_attention,
	       so.sales_order_bill_addr_line1, so.sales_order_bill_addr_line2, so.sales_order_bill_addr_suitenum,
	       so.sales_order_bill_addr_city, so.sales_order_bill_addr_state, so.sales_order_bill_addr_zip,
	       so.sales_order_bill_addr_country, so.sales_order_bill_phone, so.sales_order_bill_fax, so.sales_order_bill_email,
	       so.sales_order_ship_customer_name, so.sales_order_ship_attention,
	       so.sales_order_ship_addr_line1, so.sales_order_ship_addr_line2, so.sales_order_ship_addr_suitenum,
	       so.sales_order_ship_addr_city, so.sales_order_ship_addr_state, so.sales_order_ship_addr_zip,
	       so.sales_order_ship_addr_country, so.sales_order_ship_phone, so.sales_order_ship_fax, so.sales_order_ship_email,
	       so.sales_order_custom_fields,
	       so.sales_order_subtotal, so.sales_order_discount_total, so.sales_order_tax_total,
	       so.sales_order_shipping_charge, so.sales_order_adjustment, so.sales_order_grand_total,
	       so.sales_order_created_at, so.sales_order_updated_at
	FROM sales_order so
	JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
	JOIN customer c ON c.customer_id = so.sales_order_customer_id
	LEFT JOIN employee oe ON oe.employee_id = so.sales_order_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

func scanOrder(row pgx.Row) (*Order, error) {
	var o Order
	var customRaw []byte
	if err := row.Scan(
		&o.ID, &o.Number, &o.Status, &o.StatusCode, &o.ApprovalStatus,
		&o.Customer.ID, &o.Customer.Name, &o.OwnerUserID,
		&o.OrderDate, &o.ExpectedDelivery, &o.PaymentDueDate,
		&o.PONumber, &o.ReferenceNumber, &o.Memo,
		&o.Notes, &o.InternalNotes, &o.TermsConditions,
		&o.PaymentTermsID, &o.PriceLevelID, &o.CurrencyID,
		&o.SalesRepEmployeeID, &o.OwnerEmployeeID, &o.SalesTaxPercent,
		&o.ShipSameAsBilling,
		&o.Billing.CustomerName, &o.Billing.Attention,
		&o.Billing.AddrLine1, &o.Billing.AddrLine2, &o.Billing.SuiteUnit,
		&o.Billing.City, &o.Billing.StateID, &o.Billing.Zip,
		&o.Billing.CountryID, &o.Billing.Phone, &o.Billing.Fax, &o.Billing.Email,
		&o.Shipping.CustomerName, &o.Shipping.Attention,
		&o.Shipping.AddrLine1, &o.Shipping.AddrLine2, &o.Shipping.SuiteUnit,
		&o.Shipping.City, &o.Shipping.StateID, &o.Shipping.Zip,
		&o.Shipping.CountryID, &o.Shipping.Phone, &o.Shipping.Fax, &o.Shipping.Email,
		&customRaw,
		&o.Subtotal, &o.DiscountTotal, &o.TaxTotal,
		&o.ShippingCharge, &o.Adjustment, &o.GrandTotal,
		&o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &o.CustomFields)
	}
	return &o, nil
}

const itemSelect = `
	SELECT soi.sales_order_item_uuid, soi.line_number,
	       ii.inventory_item_uuid,
	       soi.sku, soi.item_name, soi.description, COALESCE(soi.unit_code,''),
	       soi.quantity, soi.unit_price, soi.discount_percent, soi.tax_percent,
	       soi.line_subtotal, soi.line_discount, soi.line_tax, soi.line_total,
	       soi.line_fulfilled_quantity
	FROM sales_order_item soi
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = soi.inventory_item_id
	WHERE soi.sales_order_id = $1 AND soi.item_deleted_at IS NULL
	ORDER BY soi.line_number`

func scanLine(row pgx.Rows) (Line, error) {
	var l Line
	err := row.Scan(
		&l.ID, &l.LineNumber, &l.InventoryItemID,
		&l.SKU, &l.ItemName, &l.Description, &l.UnitCode,
		&l.Quantity, &l.UnitPrice, &l.DiscountPercent, &l.TaxPercent,
		&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal,
		&l.FulfilledQuantity,
	)
	l.Status = lineStatus(l.FulfilledQuantity, l.Quantity)
	return l, err
}

// ----- reads --------------------------------------------------------------

// Get loads a single live order by its external uuid, including its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Order, error) {
	o, err := scanOrder(pool.QueryRow(ctx, orderSelect+`
		WHERE so.sales_order_uuid = $1 AND so.sales_order_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sales order: %w", err)
	}
	items, err := loadLines(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	o.Items = items
	return o, nil
}

// loadLines fetches an order's live lines by its external uuid.
func loadLines(ctx context.Context, q workflow.Querier, uuid string) ([]Line, error) {
	var internalID int
	if err := q.QueryRow(ctx,
		`SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1`, uuid).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve sales order id: %w", err)
	}
	rows, err := q.Query(ctx, itemSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load sales order items: %w", err)
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

// ----- lookups used by Create/Update ---------------------------------------

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
