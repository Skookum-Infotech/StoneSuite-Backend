package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// internalIDByUUID resolves a live invoice's internal serial id + current
// status code from its external uuid.
func internalIDByUUID(ctx context.Context, pool *pgxpool.Pool, id string) (int, string, error) {
	var internalID int
	var statusCode string
	err := pool.QueryRow(ctx, `
		SELECT i.invoice_id, rs.record_status_code
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL`, id,
	).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("resolve invoice: %w", err)
	}
	return internalID, statusCode, nil
}

// Update recomputes totals and replaces the line items of an existing invoice
// inside one transaction, then writes an 'update' history row.
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in UpdateInvoiceInput, actorEmployeeID int) (*Invoice, error) {
	internalID, statusCode, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if terminalStatuses[statusCode] {
		return nil, ClientError{Msg: "Cannot edit a " + statusCode + " invoice."}
	}
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}

	existing, err := Get(ctx, pool, id)
	if err != nil {
		return nil, err
	}

	billing := existing.Billing
	if !isZeroAddress(in.Billing) {
		billing = in.Billing
	}
	shipping := existing.Shipping
	if !isZeroAddress(in.Shipping) {
		shipping = in.Shipping
	}
	if in.ShipSameAsBilling {
		shipping = billing
	}

	resolved := make([]resolvedLine, 0, len(in.Items))
	lineMoney := make([]LineMoney, 0, len(in.Items))
	for _, item := range in.Items {
		rl, err := resolveLine(ctx, pool, item, in.SalesTaxPercent)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, rl)
		lineMoney = append(lineMoney, rl.money)
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment, existing.AmountPaid)

	// An invoice total can't be reduced below what has already been paid; that
	// would force a negative balance_due (rejected by chk_invoice_paid_nonneg).
	if existing.AmountPaid > header.GrandTotal+0.005 {
		return nil, ClientError{Msg: "Cannot reduce the invoice total below the amount already paid."}
	}

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update invoice: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	_, err = tx.Exec(ctx, `
		UPDATE invoice SET
			invoice_po_number = $1, invoice_reference_number = $2,
			invoice_date = COALESCE(NULLIF($3,'')::date, invoice_date), invoice_due_date = NULLIF($4,'')::date,
			invoice_sales_tax_percent = $5, invoice_memo = $6, invoice_notes = $7,
			invoice_internal_notes = $8, invoice_terms_conditions = $9,
			invoice_sales_rep_id = $10, invoice_owner_id = COALESCE($11, invoice_owner_id),
			invoice_payment_terms = $12, invoice_price_level = $13, invoice_currency = $14,
			invoice_subtotal = $15, invoice_discount_total = $16, invoice_tax_total = $17,
			invoice_shipping_charge = $18, invoice_adjustment = $19, invoice_grand_total = $20,
			invoice_balance_due = $21,
			invoice_bill_customer_name = $22, invoice_bill_attention = $23,
			invoice_bill_addr_line1 = $24, invoice_bill_addr_line2 = $25, invoice_bill_addr_suitenum = $26,
			invoice_bill_addr_city = $27, invoice_bill_addr_state = $28, invoice_bill_addr_zip = $29,
			invoice_bill_addr_country = $30, invoice_bill_phone = $31, invoice_bill_fax = $32, invoice_bill_email = $33,
			invoice_ship_same_as_bill = $34, invoice_ship_customer_name = $35, invoice_ship_attention = $36,
			invoice_ship_addr_line1 = $37, invoice_ship_addr_line2 = $38, invoice_ship_addr_suitenum = $39,
			invoice_ship_addr_city = $40, invoice_ship_addr_state = $41, invoice_ship_addr_zip = $42,
			invoice_ship_addr_country = $43, invoice_ship_phone = $44, invoice_ship_fax = $45, invoice_ship_email = $46,
			invoice_custom_fields = $47, invoice_updated_by = $48, invoice_updated_at = NOW(),
			invoice_record_version = invoice_record_version + 1
		WHERE invoice_id = $49`,
		in.PONumber, in.ReferenceNumber, in.InvoiceDate, in.DueDate,
		in.SalesTaxPercent, in.Memo, in.Notes, in.InternalNotes, in.TermsConditions,
		in.SalesRepEmployeeID, in.OwnerEmployeeID,
		in.PaymentTermsID, in.PriceLevelID, in.CurrencyID,
		header.Subtotal, header.DiscountTotal, header.TaxTotal,
		in.ShippingCharge, in.Adjustment, header.GrandTotal,
		header.BalanceDue,
		billing.CustomerName, billing.Attention, billing.AddrLine1, billing.AddrLine2,
		billing.SuiteUnit, billing.City, billing.StateID, billing.Zip,
		billing.CountryID, billing.Phone, billing.Fax, billing.Email,
		in.ShipSameAsBilling, shipping.CustomerName, shipping.Attention,
		shipping.AddrLine1, shipping.AddrLine2, shipping.SuiteUnit, shipping.City,
		shipping.StateID, shipping.Zip, shipping.CountryID,
		shipping.Phone, shipping.Fax, shipping.Email,
		custom, nullableInt(actorEmployeeID), internalID,
	)
	if err != nil {
		return nil, fmt.Errorf("update invoice: %w", err)
	}

	// Soft-delete the current lines (preserving line-level history on this
	// financial document) rather than hard-deleting; the partial unique index
	// uq_ii_line_active lets the same line_number be re-inserted below.
	if _, err := tx.Exec(ctx, `
		UPDATE invoice_item SET item_deleted_at = NOW()
		WHERE invoice_id = $1 AND item_deleted_at IS NULL`, internalID); err != nil {
		return nil, fmt.Errorf("clear invoice lines: %w", err)
	}
	for i, rl := range resolved {
		_, err := tx.Exec(ctx, `
			INSERT INTO invoice_item (
				invoice_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			internalID, rl.in.LineNumber, rl.invItemID,
			rl.itemName, rl.sku, rl.in.Description, rl.unitID, rl.unitCode,
			rl.in.Quantity, rl.in.UnitPrice, rl.in.DiscountPercent, rl.in.TaxRateID, rl.taxPercent,
			lineMoney[i].Subtotal, lineMoney[i].Discount, lineMoney[i].Tax, lineMoney[i].Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			return nil, fmt.Errorf("insert invoice line %d: %w", rl.in.LineNumber, err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT invoice_id, invoice_status, invoice_status, 'update', $2
		FROM invoice WHERE invoice_id = $1`, internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert invoice update history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update invoice: %w", err)
	}
	return Get(ctx, pool, id)
}

const systemEmployeeID = 1

// SoftDelete marks an invoice deleted (paired deleted_at/deleted_by).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	deletedBy := actorEmployeeID
	if deletedBy == 0 {
		deletedBy = systemEmployeeID
	}
	tag, err := pool.Exec(ctx, `
		UPDATE invoice
		SET invoice_deleted_at = NOW(), invoice_deleted_by = $1
		WHERE invoice_uuid = $2 AND invoice_deleted_at IS NULL`,
		deletedBy, id)
	if err != nil {
		return fmt.Errorf("delete invoice: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
