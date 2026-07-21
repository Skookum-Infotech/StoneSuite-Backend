// invoice/store_convert.go
package invoice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrSalesOrderNotFound is returned when the source sales order uuid matches
// no live row.
var ErrSalesOrderNotFound = errors.New("sales order not found")

// isForeignKeyViolation reports whether err is a PostgreSQL FK-constraint
// violation (code 23503) — an invalid caller-supplied reference id.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (code 23505) — the concurrent-conversion race on
// uq_invoice_sales_order_once.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// salesOrderSnapshot is the subset of a source sales order's header the
// convert path copies verbatim — including its already-frozen
// billing/shipping snapshot (spec AD-4: a snapshot is copied forward, never
// re-derived from the live customer, so later customer edits don't rewrite
// the chain).
type salesOrderSnapshot struct {
	internalID           int
	customerInternalID   int
	poNumber, refNumber  string
	memo, notes          string
	internalNotes, terms string
	paymentTermsID       *int
	priceLevelID         *int
	currencyID           *int
	salesRepEmployeeID   *int
	ownerEmployeeID      *int
	salesTaxPercent      float64
	shippingCharge       float64
	adjustment           float64
	shipSameAsBill       bool
	billing, shipping    Address
	customFields         map[string]any
}

// loadSalesOrderSnapshot loads a live sales order's header snapshot by
// external uuid inside tx.
func loadSalesOrderSnapshot(ctx context.Context, tx pgx.Tx, salesOrderUUID string) (*salesOrderSnapshot, error) {
	var s salesOrderSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT sales_order_id, sales_order_customer_id,
		       sales_order_po_number, sales_order_reference_number,
		       sales_order_memo, sales_order_notes, sales_order_internal_notes, sales_order_terms_conditions,
		       sales_order_payment_terms, sales_order_price_level, sales_order_currency,
		       sales_order_sales_rep_id, sales_order_owner_id, sales_order_sales_tax_percent,
		       sales_order_shipping_charge, sales_order_adjustment, sales_order_ship_same_as_bill,
		       sales_order_bill_customer_name, sales_order_bill_attention,
		       sales_order_bill_addr_line1, sales_order_bill_addr_line2, sales_order_bill_addr_suitenum,
		       sales_order_bill_addr_city, sales_order_bill_addr_state, sales_order_bill_addr_zip,
		       sales_order_bill_addr_country, sales_order_bill_phone, sales_order_bill_fax, sales_order_bill_email,
		       sales_order_ship_customer_name, sales_order_ship_attention,
		       sales_order_ship_addr_line1, sales_order_ship_addr_line2, sales_order_ship_addr_suitenum,
		       sales_order_ship_addr_city, sales_order_ship_addr_state, sales_order_ship_addr_zip,
		       sales_order_ship_addr_country, sales_order_ship_phone, sales_order_ship_fax, sales_order_ship_email,
		       sales_order_custom_fields
		FROM sales_order WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL`, salesOrderUUID).Scan(
		&s.internalID, &s.customerInternalID,
		&s.poNumber, &s.refNumber,
		&s.memo, &s.notes, &s.internalNotes, &s.terms,
		&s.paymentTermsID, &s.priceLevelID, &s.currencyID,
		&s.salesRepEmployeeID, &s.ownerEmployeeID, &s.salesTaxPercent,
		&s.shippingCharge, &s.adjustment, &s.shipSameAsBill,
		&s.billing.CustomerName, &s.billing.Attention,
		&s.billing.AddrLine1, &s.billing.AddrLine2, &s.billing.SuiteUnit,
		&s.billing.City, &s.billing.StateID, &s.billing.Zip,
		&s.billing.CountryID, &s.billing.Phone, &s.billing.Fax, &s.billing.Email,
		&s.shipping.CustomerName, &s.shipping.Attention,
		&s.shipping.AddrLine1, &s.shipping.AddrLine2, &s.shipping.SuiteUnit,
		&s.shipping.City, &s.shipping.StateID, &s.shipping.Zip,
		&s.shipping.CountryID, &s.shipping.Phone, &s.shipping.Fax, &s.shipping.Email,
		&customRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrSalesOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sales order snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}

// salesOrderSourceLine is one live sales_order_item row's frozen values,
// copied verbatim (not re-priced) into the new invoice's lines.
type salesOrderSourceLine struct {
	internalID          int
	lineNumber          int
	inventoryItemID     *int
	itemName, sku, desc string
	unitID              *int
	unitCode            string
	quantity            float64
	unitPrice           float64
	discountPercent     float64
	taxRateID           *int
	taxPercent          float64
	money               LineMoney
}

// loadSalesOrderSourceLines loads a live sales order's lines by its internal id.
func loadSalesOrderSourceLines(ctx context.Context, tx pgx.Tx, salesOrderInternalID int) ([]salesOrderSourceLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT sales_order_item_id, line_number, inventory_item_id,
		       item_name, sku, description, unit_id, COALESCE(unit_code,''),
		       quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
		       line_subtotal, line_discount, line_tax, line_total
		FROM sales_order_item
		WHERE sales_order_id = $1 AND item_deleted_at IS NULL
		ORDER BY line_number`, salesOrderInternalID)
	if err != nil {
		return nil, fmt.Errorf("load sales order lines: %w", err)
	}
	defer rows.Close()
	out := []salesOrderSourceLine{}
	for rows.Next() {
		var l salesOrderSourceLine
		if err := rows.Scan(
			&l.internalID, &l.lineNumber, &l.inventoryItemID,
			&l.itemName, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.unitPrice, &l.discountPercent, &l.taxRateID, &l.taxPercent,
			&l.money.Subtotal, &l.money.Discount, &l.money.Tax, &l.money.Total,
		); err != nil {
			return nil, fmt.Errorf("scan sales order line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ClientError{Msg: "Sales order has no line items to convert."}
	}
	return out, nil
}

// insertConvertedLines bulk-inserts sales-order-sourced lines as invoice_item
// rows, stamping sales_order_item_id for per-line lineage (spec AD-6).
// invoice_item already carries this FK column, so — unlike Quote→SalesOrder
// (whose target, sales_order_item, has no such column and needs the separate
// quote_conversion join table for its line mapping) — no extra table is
// needed here.
func insertConvertedLines(ctx context.Context, tx pgx.Tx, invoiceInternalID int, lines []salesOrderSourceLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO invoice_item (
				invoice_id, line_number, inventory_item_id, sales_order_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19)`,
			invoiceInternalID, l.lineNumber, l.inventoryItemID, l.internalID,
			l.itemName, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			return fmt.Errorf("insert converted invoice item: %w", err)
		}
	}
	return nil
}

// ConvertFromSalesOrder creates a new Invoice as a full snapshot copy of a
// live sales order's header + lines (spec AD-6): every line item is copied
// verbatim (not re-priced against current catalog data), header totals
// (including the AR balance, which starts fully unpaid) are recomputed from
// the copied lines via invoice's own calc, and the new invoice is linked
// back via invoice_sales_order_id.
//
// A sales order may only convert once (uq_invoice_sales_order_once).
// Replaying the call after a successful conversion returns the existing
// invoice and created=false instead of erroring, so the endpoint is safe to
// retry.
func ConvertFromSalesOrder(ctx context.Context, pool *pgxpool.Pool, salesOrderUUID string, actorEmployeeID int) (inv *Invoice, created bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("begin convert sales order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	src, err := loadSalesOrderSnapshot(ctx, tx, salesOrderUUID)
	if err != nil {
		return nil, false, err
	}

	var existingUUID string
	err = tx.QueryRow(ctx,
		`SELECT invoice_uuid FROM invoice WHERE invoice_sales_order_id = $1 AND invoice_deleted_at IS NULL`,
		src.internalID).Scan(&existingUUID)
	if err == nil {
		existing, gerr := Get(ctx, pool, existingUUID)
		if gerr != nil {
			return nil, false, gerr
		}
		return existing, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, fmt.Errorf("check existing conversion: %w", err)
	}

	lines, err := loadSalesOrderSourceLines(ctx, tx, src.internalID)
	if err != nil {
		return nil, false, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, src.shippingCharge, src.adjustment, 0) // amountPaid starts at 0

	typeID, err := typeIDByCodeTx(ctx, tx, "INVC")
	if err != nil {
		return nil, false, err
	}
	draftStatusID, err := statusIDByCodeTx(ctx, tx, typeID, "DRFT")
	if err != nil {
		return nil, false, err
	}

	ownerEmployeeID := src.ownerEmployeeID
	if ownerEmployeeID == nil && actorEmployeeID != 0 {
		ownerEmployeeID = &actorEmployeeID
	}

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO invoice (
			record_type, invoice_status, invoice_customer_id, invoice_sales_order_id,
			invoice_po_number, invoice_reference_number, invoice_date,
			invoice_sales_tax_percent, invoice_memo, invoice_notes, invoice_internal_notes, invoice_terms_conditions,
			invoice_sales_rep_id, invoice_owner_id,
			invoice_payment_terms, invoice_price_level, invoice_currency,
			invoice_subtotal, invoice_discount_total, invoice_tax_total,
			invoice_shipping_charge, invoice_adjustment, invoice_grand_total,
			invoice_amount_paid, invoice_balance_due,
			invoice_bill_customer_name, invoice_bill_attention, invoice_bill_addr_line1, invoice_bill_addr_line2,
			invoice_bill_addr_suitenum, invoice_bill_addr_city, invoice_bill_addr_state, invoice_bill_addr_zip,
			invoice_bill_addr_country, invoice_bill_phone, invoice_bill_fax, invoice_bill_email,
			invoice_ship_same_as_bill, invoice_ship_customer_name, invoice_ship_attention,
			invoice_ship_addr_line1, invoice_ship_addr_line2, invoice_ship_addr_suitenum, invoice_ship_addr_city,
			invoice_ship_addr_state, invoice_ship_addr_zip, invoice_ship_addr_country,
			invoice_ship_phone, invoice_ship_fax, invoice_ship_email,
			invoice_custom_fields, invoice_created_by, invoice_updated_by
		) VALUES (
			$1,$2,$3,$4, $5,$6,CURRENT_DATE, $7,$8,$9,$10,$11, $12,$13, $14,$15,$16,
			$17,$18,$19, $20,$21,$22,
			$23,$24,
			$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,
			$37,$38,$39,$40,$41,$42,$43,$44,$45,$46,$47,$48,$49,
			$50,$51,$51
		) RETURNING invoice_id, invoice_uuid`,
		typeID, draftStatusID, src.customerInternalID, src.internalID,
		src.poNumber, src.refNumber,
		src.salesTaxPercent, src.memo, src.notes, src.internalNotes, src.terms,
		src.salesRepEmployeeID, ownerEmployeeID,
		src.paymentTermsID, src.priceLevelID, src.currencyID,
		header.Subtotal, header.DiscountTotal, header.TaxTotal,
		src.shippingCharge, src.adjustment, header.GrandTotal,
		header.AmountPaid, header.BalanceDue,
		src.billing.CustomerName, src.billing.Attention, src.billing.AddrLine1, src.billing.AddrLine2,
		src.billing.SuiteUnit, src.billing.City, src.billing.StateID, src.billing.Zip,
		src.billing.CountryID, src.billing.Phone, src.billing.Fax, src.billing.Email,
		src.shipSameAsBill, src.shipping.CustomerName, src.shipping.Attention,
		src.shipping.AddrLine1, src.shipping.AddrLine2, src.shipping.SuiteUnit, src.shipping.City,
		src.shipping.StateID, src.shipping.Zip, src.shipping.CountryID,
		src.shipping.Phone, src.shipping.Fax, src.shipping.Email,
		src.customFields, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		if isUniqueViolation(err) {
			// Lost a concurrent-conversion race: fetch and return the winner.
			existing, _, gerr := ConvertFromSalesOrder(ctx, pool, salesOrderUUID, actorEmployeeID)
			return existing, false, gerr
		}
		if isForeignKeyViolation(err) {
			return nil, false, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, false, fmt.Errorf("insert converted invoice: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE invoice SET invoice_number = $1 WHERE invoice_id = $2`, number, newID); err != nil {
		return nil, false, fmt.Errorf("set invoice number: %w", err)
	}

	if err := insertConvertedLines(ctx, tx, newID, lines, actorEmployeeID); err != nil {
		return nil, false, err
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, draftStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, false, fmt.Errorf("insert invoice create history: %w", err)
	}

	// Mark the source sales order's own history with the conversion (schema's
	// sales_order_history.action was widened to allow 'convert' for this).
	if _, err := tx.Exec(ctx, `
		INSERT INTO sales_order_history (sales_order_id, action, actor_employee_id, snapshot)
		VALUES ($1, 'convert', $2, jsonb_build_object('invoiceId', $3::int, 'invoiceUuid', $4::text))`,
		src.internalID, nullableInt(actorEmployeeID), newID, newUUID); err != nil {
		return nil, false, fmt.Errorf("insert sales order convert history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, false, fmt.Errorf("commit convert sales order: %w", err)
	}
	got, err := Get(ctx, pool, newUUID)
	if err != nil {
		return nil, false, err
	}
	return got, true, nil
}

// typeIDByCodeTx is typeIDByCode against a transaction instead of the pool
// (convert needs the lookup inside its own tx, unlike Create's pre-tx call).
func typeIDByCodeTx(ctx context.Context, tx pgx.Tx, code string) (int, error) {
	var id int
	if err := tx.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve record type %s: %w", code, err)
	}
	return id, nil
}

// statusIDByCodeTx is statusIDByCode against a transaction instead of the pool.
func statusIDByCodeTx(ctx context.Context, tx pgx.Tx, typeID int, code string) (int, error) {
	var id int
	if err := tx.QueryRow(ctx,
		`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve status %s: %w", code, err)
	}
	return id, nil
}
