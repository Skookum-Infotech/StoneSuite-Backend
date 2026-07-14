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
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
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

// customerSnapshot loads a customer's internal id, name, and default
// billing/shipping address blocks for the create-time snapshot (spec AD-4).
func customerSnapshot(ctx context.Context, q workflow.Querier, customerUUID string) (id int, name string, billing, shipping AddressInput, err error) {
	var (
		billLine1, billLine2, billSuite, billCity string
		billState, billCountry                    *int
		billZip                                   string
		shipLine1, shipLine2, shipSuite, shipCity string
		shipState, shipCountry                    *int
		shipZip                                   string
	)
	err = q.QueryRow(ctx, `
		SELECT customer_id, customer_name,
		       customer_bill_addr_line1, customer_bill_addr_line2, customer_bill_addr_suitenum,
		       customer_bill_addr_city, customer_bill_addr_state, customer_bill_addr_zip, customer_bill_addr_country,
		       customer_ship_addr_line1, customer_ship_addr_line2, customer_ship_addr_suitenum,
		       customer_ship_addr_city, customer_ship_addr_state, customer_ship_addr_zip, customer_ship_addr_country
		FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID).Scan(
		&id, &name,
		&billLine1, &billLine2, &billSuite, &billCity, &billState, &billZip, &billCountry,
		&shipLine1, &shipLine2, &shipSuite, &shipCity, &shipState, &shipZip, &shipCountry,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", AddressInput{}, AddressInput{}, ClientError{Msg: "Unknown customer."}
	}
	if err != nil {
		return 0, "", AddressInput{}, AddressInput{}, fmt.Errorf("load customer snapshot: %w", err)
	}
	billing = AddressInput{
		CustomerName: name, AddrLine1: billLine1, AddrLine2: billLine2, SuiteUnit: billSuite,
		City: billCity, StateID: billState, Zip: billZip, CountryID: billCountry,
	}
	shipping = AddressInput{
		CustomerName: name, AddrLine1: shipLine1, AddrLine2: shipLine2, SuiteUnit: shipSuite,
		City: shipCity, StateID: shipState, Zip: shipZip, CountryID: shipCountry,
	}
	return id, name, billing, shipping, nil
}

// itemSnapshot is what a line needs from its catalog item at add time.
type itemSnapshot struct {
	internalID int
	sku        string
	name       string
	desc       string
	unitID     *int
	unitCode   string
	unitPrice  float64
	taxRateID  *int
}

// resolveInventoryItem loads a catalog item's snapshot fields by its external
// uuid. Returns ClientError when the uuid does not resolve to a live item.
func resolveInventoryItem(ctx context.Context, q workflow.Querier, uuid string) (*itemSnapshot, error) {
	var s itemSnapshot
	err := q.QueryRow(ctx, `
		SELECT ii.inventory_item_id, ii.inventory_item_sku, ii.inventory_item_name, ii.inventory_item_description,
		       ii.inventory_item_unit_id, COALESCE(u.unit_code,''), ii.inventory_item_unit_price, ii.inventory_item_tax_rate_id
		FROM inventory_item ii
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_uuid = $1 AND ii.inventory_item_deleted_at IS NULL`, uuid).Scan(
		&s.internalID, &s.sku, &s.name, &s.desc, &s.unitID, &s.unitCode, &s.unitPrice, &s.taxRateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown inventory item: " + uuid}
	}
	if err != nil {
		return nil, fmt.Errorf("load inventory item: %w", err)
	}
	return &s, nil
}

// taxPercentForRate loads a named tax rate's percent by internal id.
func taxPercentForRate(ctx context.Context, q workflow.Querier, taxRateID int) (float64, error) {
	var pct float64
	if err := q.QueryRow(ctx,
		`SELECT tax_rate_percent FROM lkp_tax_rate WHERE tax_rate_id = $1`, taxRateID).Scan(&pct); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ClientError{Msg: "Unknown tax rate."}
		}
		return 0, fmt.Errorf("load tax rate: %w", err)
	}
	return pct, nil
}

// resolvedLine is a line after catalog/free-text resolution, ready to price
// and insert.
type resolvedLine struct {
	lineNumber      int
	inventoryItemID *int // internal FK, nil for free-text
	warehouseID     *int
	sku, name, desc string
	unitID          *int
	unitCode        string
	quantity        float64
	unitPrice       float64
	discountPercent float64
	taxRateID       *int
	taxPercent      float64
	money           LineMoney
}

// resolveLines validates and resolves every input line against the catalog
// (or free text), computing each line's stored money (spec §9). headerTax is
// the header's default tax percent, used when a line has no tax rate.
func resolveLines(ctx context.Context, q workflow.Querier, items []LineInput2, headerTax float64) ([]resolvedLine, error) {
	if len(items) == 0 {
		return nil, ClientError{Msg: "At least one line item is required."}
	}
	out := make([]resolvedLine, 0, len(items))
	seenLine := map[int]bool{}
	for _, in := range items {
		if in.LineNumber <= 0 {
			return nil, ClientError{Msg: "Each line item needs a positive line number."}
		}
		if seenLine[in.LineNumber] {
			return nil, ClientError{Msg: fmt.Sprintf("Duplicate line number %d.", in.LineNumber)}
		}
		seenLine[in.LineNumber] = true
		if in.Quantity <= 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: quantity must be greater than zero.", in.LineNumber)}
		}
		if in.UnitPrice < 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: unit price cannot be negative.", in.LineNumber)}
		}
		if in.DiscountPercent < 0 || in.DiscountPercent > 100 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: discount percent must be between 0 and 100.", in.LineNumber)}
		}

		rl := resolvedLine{
			lineNumber:      in.LineNumber,
			warehouseID:     in.WarehouseID,
			quantity:        in.Quantity,
			unitPrice:       in.UnitPrice,
			discountPercent: in.DiscountPercent,
			taxRateID:       in.TaxRateID,
		}

		if in.InventoryItemUUID != "" {
			item, err := resolveInventoryItem(ctx, q, in.InventoryItemUUID)
			if err != nil {
				return nil, err
			}
			id := item.internalID
			rl.inventoryItemID = &id
			rl.sku, rl.name, rl.desc = item.sku, item.name, item.desc
			rl.unitID, rl.unitCode = item.unitID, item.unitCode
			if rl.unitPrice == 0 {
				rl.unitPrice = item.unitPrice
			}
			if rl.taxRateID == nil {
				rl.taxRateID = item.taxRateID
			}
		} else if strings.TrimSpace(in.Description) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: either an inventory item or a description is required.", in.LineNumber)}
		} else {
			rl.desc = in.Description
			rl.sku = strings.TrimSpace(in.SKU)
			rl.name = strings.TrimSpace(in.ItemName)
			rl.unitCode = strings.TrimSpace(in.UnitCode)
		}

		if rl.taxRateID != nil {
			pct, err := taxPercentForRate(ctx, q, *rl.taxRateID)
			if err != nil {
				return nil, err
			}
			rl.taxPercent = pct
		} else if in.TaxPercent != nil {
			if *in.TaxPercent < 0 || *in.TaxPercent > 100 {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d: tax percent must be between 0 and 100.", in.LineNumber)}
			}
			rl.taxPercent = *in.TaxPercent
		} else {
			rl.taxPercent = headerTax
		}

		rl.money = ComputeLine(LineInput{
			Quantity: rl.quantity, UnitPrice: rl.unitPrice,
			DiscountPercent: rl.discountPercent, TaxPercent: rl.taxPercent,
		})
		out = append(out, rl)
	}
	return out, nil
}

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// colVal pairs a column name with its bind value (and an optional type cast
// suffix, e.g. "::date") so an INSERT/UPDATE's column list and argument list
// are always built from the same slice — never two hand-aligned lists that
// can silently drift out of position against each other.
type colVal struct {
	col  string
	val  any
	cast string
}

// addrColVals returns the 12 (column, value) pairs for a billing/shipping
// address block, in the exact column order the schema declares (state before
// zip — see sales_order_bill_addr_state/_zip in schema.sql). prefix is
// "sales_order_bill" or "sales_order_ship".
func addrColVals(prefix string, a AddressInput) []colVal {
	return []colVal{
		{prefix + "_customer_name", a.CustomerName, ""},
		{prefix + "_attention", a.Attention, ""},
		{prefix + "_addr_line1", a.AddrLine1, ""},
		{prefix + "_addr_line2", a.AddrLine2, ""},
		{prefix + "_addr_suitenum", a.SuiteUnit, ""},
		{prefix + "_addr_city", a.City, ""},
		{prefix + "_addr_state", a.StateID, ""},
		{prefix + "_addr_zip", a.Zip, ""},
		{prefix + "_addr_country", a.CountryID, ""},
		{prefix + "_phone", a.Phone, ""},
		{prefix + "_fax", a.Fax, ""},
		{prefix + "_email", a.Email, ""},
	}
}

// buildInsert renders an INSERT ... VALUES (...) RETURNING statement from
// column/value pairs, numbering placeholders by position so cols and args
// can never drift apart.
func buildInsert(table string, cv []colVal, returning string) (string, []any) {
	cols := make([]string, len(cv))
	phs := make([]string, len(cv))
	args := make([]any, len(cv))
	for i, c := range cv {
		cols[i] = c.col
		args[i] = c.val
		phs[i] = fmt.Sprintf("$%d%s", i+1, c.cast)
	}
	sql := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	if returning != "" {
		sql += " RETURNING " + returning
	}
	return sql, args
}

// buildUpdateSet renders an "UPDATE ... SET col=$n, ... WHERE <where>"
// statement. leadingArgs are bound first (e.g. the WHERE clause's own
// placeholders, referenced as $1.. in where); cv's placeholders continue
// after them.
func buildUpdateSet(table string, leadingArgs []any, cv []colVal, extraSets []string, where string) (string, []any) {
	sets := make([]string, 0, len(cv)+len(extraSets))
	args := make([]any, 0, len(leadingArgs)+len(cv))
	args = append(args, leadingArgs...)
	for _, c := range cv {
		args = append(args, c.val)
		sets = append(sets, fmt.Sprintf("%s = $%d%s", c.col, len(args), c.cast))
	}
	sets = append(sets, extraSets...)
	sql := "UPDATE " + table + " SET " + strings.Join(sets, ", ") + " WHERE " + where
	return sql, args
}

// ----- Create ---------------------------------------------------------------

// Create inserts a new sales order (header + lines) inside one transaction:
// snapshots billing/shipping from the customer (unless overridden), resolves
// and prices every line, computes header totals, assigns the order number,
// and starts the order at DRFT (spec §10, AD-4, AD-7).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateOrderInput, actorEmployeeID int) (*Order, error) {
	if strings.TrimSpace(in.CustomerUUID) == "" {
		return nil, ClientError{Msg: "A customer is required."}
	}
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "Sales tax percent must be between 0 and 100."}
	}
	// CustomFields is stored as-is, unvalidated: there is no admin-configurable
	// field-definition set for the relational sales_order module yet. It must
	// NOT be validated against the workflow keyed "sales_order" — that is an
	// unrelated legacy v1 JSONB placeholder workflow (schema.sql migration
	// 000010) whose field defs (customer_name required, etc.) predate this
	// module and collide only by sharing the same key string.

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create sales order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	custInternalID, custName, defBilling, defShipping, err := customerSnapshot(ctx, tx, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	billing := overrideAddress(defBilling, in.Billing)
	var shipping AddressInput
	if in.ShipSameAsBilling {
		shipping = billing
	} else {
		shipping = overrideAddress(defShipping, in.Shipping)
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, sordRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	dueDate, err := resolvePaymentDueDate(ctx, tx, in.OrderDate, in.PaymentDueDate, in.PaymentTermsID)
	if err != nil {
		return nil, err
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"sales_order_status", draftStatusID, ""},
		{"sales_order_customer_id", custInternalID, ""},
		{"sales_order_po_number", in.PONumber, ""},
		{"sales_order_reference_number", in.ReferenceNumber, ""},
		{"sales_order_date", orNow(in.OrderDate), "::date"},
		{"sales_order_expected_delivery", nullableDate(in.ExpectedDelivery), "::date"},
		{"sales_order_payment_due_date", dueDate, "::date"},
		{"sales_order_sales_tax_percent", in.SalesTaxPercent, ""},
		{"sales_order_memo", in.Memo, ""},
		{"sales_order_notes", in.Notes, ""},
		{"sales_order_internal_notes", in.InternalNotes, ""},
		{"sales_order_terms_conditions", in.TermsConditions, ""},
		{"sales_order_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"sales_order_owner_id", nullableInt(ownerEmployeeID), ""},
		{"sales_order_payment_terms", in.PaymentTermsID, ""},
		{"sales_order_price_level", in.PriceLevelID, ""},
		{"sales_order_currency", in.CurrencyID, ""},
		{"sales_order_subtotal", header.Subtotal, ""},
		{"sales_order_discount_total", header.DiscountTotal, ""},
		{"sales_order_tax_total", header.TaxTotal, ""},
		{"sales_order_shipping_charge", in.ShippingCharge, ""},
		{"sales_order_adjustment", in.Adjustment, ""},
		{"sales_order_grand_total", header.GrandTotal, ""},
		{"sales_order_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"sales_order_custom_fields", custom, ""},
		{"sales_order_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals("sales_order_bill", billing)...)
	cv = append(cv, addrColVals("sales_order_ship", shipping)...)

	insertSQL, insertArgs := buildInsert("sales_order", cv, "sales_order_id, sales_order_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		if isCheckViolation(err) {
			return nil, ClientError{Msg: "One or more sales order values are out of range."}
		}
		return nil, fmt.Errorf("insert sales order: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE sales_order SET sales_order_number = $1 WHERE sales_order_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set sales order number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create sales order: %w", err)
	}
	_ = custName // used only to build the snapshot above
	return Get(ctx, pool, newUUID)
}

// overrideAddress layers a caller-supplied partial address over a default
// (e.g. the customer's snapshot), preferring the override for any non-empty
// field.
func overrideAddress(def, override AddressInput) AddressInput {
	out := def
	if override.CustomerName != "" {
		out.CustomerName = override.CustomerName
	}
	if override.Attention != "" {
		out.Attention = override.Attention
	}
	if override.AddrLine1 != "" {
		out.AddrLine1 = override.AddrLine1
	}
	if override.AddrLine2 != "" {
		out.AddrLine2 = override.AddrLine2
	}
	if override.SuiteUnit != "" {
		out.SuiteUnit = override.SuiteUnit
	}
	if override.City != "" {
		out.City = override.City
	}
	if override.StateID != nil {
		out.StateID = override.StateID
	}
	if override.Zip != "" {
		out.Zip = override.Zip
	}
	if override.CountryID != nil {
		out.CountryID = override.CountryID
	}
	if override.Phone != "" {
		out.Phone = override.Phone
	}
	if override.Fax != "" {
		out.Fax = override.Fax
	}
	if override.Email != "" {
		out.Email = override.Email
	}
	return out
}

// orNow returns the given "yyyy-mm-dd" date string, or today when blank
// (sales_order_date has a CURRENT_DATE default, but Create always supplies an
// explicit value so the stored and returned dates agree).
func orNow(d string) string {
	if d == "" {
		return "now"
	}
	return d
}

// nullableDate returns the given "yyyy-mm-dd" string as a nullable date arg.
func nullableDate(d string) any {
	if d == "" {
		return nil
	}
	return d
}

// resolvePaymentDueDate computes the sales_order_payment_due_date arg (AD-8,
// schema.org paymentDueDate): an explicit caller value wins (and must be on or
// after the order date); otherwise the order date plus the payment term's
// net-days; otherwise NULL. orderDate is the raw request value (blank ⇒ today,
// matching orNow). Returns a nullable "yyyy-mm-dd" date arg.
func resolvePaymentDueDate(ctx context.Context, q workflow.Querier, orderDate, explicit string, paymentTermsID *int) (any, error) {
	base := time.Now()
	if od := strings.TrimSpace(orderDate); od != "" && od != "now" {
		parsed, perr := time.Parse("2006-01-02", od)
		if perr != nil {
			return nil, ClientError{Msg: "Order date must be in yyyy-mm-dd format."}
		}
		base = parsed
	}
	if s := strings.TrimSpace(explicit); s != "" {
		due, perr := time.Parse("2006-01-02", s)
		if perr != nil {
			return nil, ClientError{Msg: "Payment due date must be in yyyy-mm-dd format."}
		}
		if due.Before(base) {
			return nil, ClientError{Msg: "Payment due date cannot be before the order date."}
		}
		return s, nil
	}
	if paymentTermsID == nil || *paymentTermsID <= 0 {
		return nil, nil
	}
	var netDays int
	err := q.QueryRow(ctx,
		`SELECT payment_terms_net_days FROM lkp_payment_terms WHERE payment_terms_id = $1`, *paymentTermsID).Scan(&netDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // invalid terms id is reported by the header FK violation
	}
	if err != nil {
		return nil, fmt.Errorf("load payment terms net days: %w", err)
	}
	return base.AddDate(0, 0, netDays).Format("2006-01-02"), nil
}

// insertLines bulk-inserts resolved lines as sales_order_item rows.
func insertLines(ctx context.Context, tx pgx.Tx, orderInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO sales_order_item (
				sales_order_id, line_number, inventory_item_id, warehouse_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19)`,
			orderInternalID, l.lineNumber, l.inventoryItemID, l.warehouseID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit, tax rate, or warehouse was referenced.", l.lineNumber)}
			}
			if isCheckViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: one or more values are out of range.", l.lineNumber)}
			}
			return fmt.Errorf("insert sales order item: %w", err)
		}
	}
	return nil
}

// writeHistory appends a sales_order_history row. Best-effort: failures are
// logged-and-swallowed by the caller's transaction commit path (a failed
// history write should not be silently ignored inside a still-open tx, so
// this returns the error for the caller to propagate, unlike crmstore's
// fire-and-forget writeHistory which runs outside any transaction).
func writeHistory(ctx context.Context, tx pgx.Tx, orderInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO sales_order_history (sales_order_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		orderInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// ----- Update ---------------------------------------------------------------

// Update replaces a live order's header fields and lines (recomputing
// totals) inside one transaction. Rejected once the order has reached a
// terminal status (FILL/CANC) — a filled or cancelled order is immutable.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateOrderInput, actorEmployeeID int) (*Order, error) {
	// See Create: CustomFields is intentionally not validated against the
	// unrelated legacy "sales_order" v1 workflow.
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "Sales tax percent must be between 0 and 100."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update sales order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, custInternalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT so.sales_order_id, so.sales_order_customer_id, rs.record_status_code
		FROM sales_order so JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		WHERE so.sales_order_uuid = $1 AND so.sales_order_deleted_at IS NULL`, uuid,
	).Scan(&internalID, &custInternalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sales order for update: %w", err)
	}
	if statusCode == "FILL" || statusCode == "CANC" {
		return nil, ClientError{Msg: "A filled or cancelled order cannot be edited."}
	}

	_, custName, defBilling, defShipping, err := customerSnapshotByInternalID(ctx, tx, custInternalID)
	if err != nil {
		return nil, err
	}
	billing := overrideAddress(defBilling, in.Billing)
	var shipping AddressInput
	if in.ShipSameAsBilling {
		shipping = billing
	} else {
		shipping = overrideAddress(defShipping, in.Shipping)
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment)

	dueDate, err := resolvePaymentDueDate(ctx, tx, in.OrderDate, in.PaymentDueDate, in.PaymentTermsID)
	if err != nil {
		return nil, err
	}

	cv := []colVal{
		{"sales_order_po_number", in.PONumber, ""},
		{"sales_order_reference_number", in.ReferenceNumber, ""},
		{"sales_order_date", orNow(in.OrderDate), "::date"},
		{"sales_order_expected_delivery", nullableDate(in.ExpectedDelivery), "::date"},
		{"sales_order_payment_due_date", dueDate, "::date"},
		{"sales_order_sales_tax_percent", in.SalesTaxPercent, ""},
		{"sales_order_memo", in.Memo, ""},
		{"sales_order_notes", in.Notes, ""},
		{"sales_order_internal_notes", in.InternalNotes, ""},
		{"sales_order_terms_conditions", in.TermsConditions, ""},
		{"sales_order_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"sales_order_owner_id", in.OwnerEmployeeID, ""},
		{"sales_order_payment_terms", in.PaymentTermsID, ""},
		{"sales_order_price_level", in.PriceLevelID, ""},
		{"sales_order_currency", in.CurrencyID, ""},
		{"sales_order_subtotal", header.Subtotal, ""},
		{"sales_order_discount_total", header.DiscountTotal, ""},
		{"sales_order_tax_total", header.TaxTotal, ""},
		{"sales_order_shipping_charge", in.ShippingCharge, ""},
		{"sales_order_adjustment", in.Adjustment, ""},
		{"sales_order_grand_total", header.GrandTotal, ""},
		{"sales_order_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"sales_order_custom_fields", in.CustomFields, ""},
	}
	cv = append(cv, addrColVals("sales_order_bill", billing)...)
	cv = append(cv, addrColVals("sales_order_ship", shipping)...)
	cv = append(cv, colVal{"sales_order_updated_by", nullableInt(actorEmployeeID), ""})

	updateSQL, updateArgs := buildUpdateSet("sales_order", []any{uuid}, cv,
		[]string{"sales_order_updated_at = NOW()", "sales_order_record_version = sales_order_record_version + 1"},
		"sales_order_uuid = $1 AND sales_order_deleted_at IS NULL")
	_, err = tx.Exec(ctx, updateSQL, updateArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		if isCheckViolation(err) {
			return nil, ClientError{Msg: "One or more sales order values are out of range."}
		}
		return nil, fmt.Errorf("update sales order: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE sales_order_item SET item_deleted_at = NOW() WHERE sales_order_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous sales order items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update sales order: %w", err)
	}
	_ = custName
	return Get(ctx, pool, uuid)
}

// customerSnapshotByInternalID is customerSnapshot keyed by the internal id
// already resolved on the order row (Update doesn't re-resolve by uuid).
func customerSnapshotByInternalID(ctx context.Context, q workflow.Querier, custInternalID int) (id int, name string, billing, shipping AddressInput, err error) {
	var uuid string
	if err := q.QueryRow(ctx, `SELECT customer_uuid FROM customer WHERE customer_id = $1`, custInternalID).Scan(&uuid); err != nil {
		return 0, "", AddressInput{}, AddressInput{}, fmt.Errorf("resolve customer uuid: %w", err)
	}
	return customerSnapshot(ctx, q, uuid)
}

// ----- SoftDelete ------------------------------------------------------------

// SoftDelete marks a live order deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE sales_order
		SET sales_order_deleted_at = NOW(), sales_order_deleted_by = $2
		WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete sales order: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ----- Transition ------------------------------------------------------------

// Transition moves a live order to toStatusCode, validating the move against
// the static transition map (spec §8), row-locking the order to serialize
// concurrent transitions, writing a history row, and — on cancellation —
// releasing any open inventory allocations (spec §8, Task 6.1).
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Order, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT so.sales_order_id, so.sales_order_status, rs.record_status_code, so.sales_order_approval_status
		FROM sales_order so JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		WHERE so.sales_order_uuid = $1 AND so.sales_order_deleted_at IS NULL
		FOR UPDATE OF so`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sales order for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, sordRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-10 approval gate: an order may not leave a status that has configured
	// approvers until it has been approved. Statuses with no approvers never block.
	requiredHere, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if requiredHere > 0 && approvalStatus != approvalApproved {
		return nil, ErrApprovalRequired
	}
	// The status being entered may itself require approval → start it pending.
	targetApprovers, err := activeApproverCount(ctx, tx, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalPending
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sales_order SET
			sales_order_status = $2, sales_order_approval_status = $4, sales_order_approved_by = NULL,
			sales_order_updated_at = NOW(),
			sales_order_updated_by = $3, sales_order_record_version = sales_order_record_version + 1
		WHERE sales_order_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition sales order: %w", err)
	}

	if toStatusCode == "CANC" {
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_allocation SET allocation_status = 'released', allocation_updated_at = NOW()
			WHERE sales_order_id = $1 AND allocation_status IN ('reserved','partially_fulfilled')`, internalID); err != nil {
			return nil, fmt.Errorf("release allocations on cancel: %w", err)
		}
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// ----- Search ----------------------------------------------------------------

// Search lists orders under the caller's RBAC scope with filter/sort/global
// search + keyset pagination, mirroring relationalStore.SearchRecords. List
// rows omit line items (spec: avoid an N+1 join on list).
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"so.sales_order_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope == "own" || scope == "team" {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("so.sales_order_owner_id = $%d", nextIdx))
		args = append(args, empID)
		nextIdx++
	}

	built, err := query.Build(req, resolver{}, nextIdx)
	if err != nil {
		return Page{}, err
	}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}
	args = append(args, built.Args...)

	q := orderSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search sales orders: %w", err)
	}
	defer rows.Close()
	out := []Order{}
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *o)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search sales orders: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, built.Sort.Field))
	}
	return page, nil
}

// sortValue reads the effective sort field's value from an order to mint the
// next cursor.
func sortValue(o Order, field string) any {
	switch field {
	case "updated_at":
		return o.UpdatedAt
	case "grand_total":
		return o.GrandTotal
	case "order_date":
		return o.OrderDate
	case "document_number", "record_number":
		return o.Number
	default: // created_at (default)
		return o.CreatedAt
	}
}

// employeeIDByIdentity resolves a control-plane identity to a tenant
// employee_id, mirroring crmstore.relationalStore.employeeIDByIdentity.
func employeeIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (int, bool) {
	if identityID == "" {
		return 0, false
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT e.employee_id FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE u.identity_id = $1 AND e.employee_deleted_at IS NULL`, identityID).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}
