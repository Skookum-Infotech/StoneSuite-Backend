package creditmemo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// customerSnapshot is the subset of customer columns needed to default the
// billing snapshot at credit memo creation. A credit memo has no shipping
// snapshot: nothing is shipped when credit is issued.
type customerSnapshot struct {
	name, email, phone, fax                   string
	primaryLine1, primaryLine2, primarySuite  string
	primaryCity                               string
	primaryState, primaryCountry              *int
	primaryZip                                string
	billAsPrimary                             bool
	billLine1, billLine2, billSuite, billCity string
	billState, billCountry                    *int
	billZip                                   string
}

func loadCustomerSnapshot(ctx context.Context, pool *pgxpool.Pool, customerUUID string) (int, customerSnapshot, error) {
	var id int
	var s customerSnapshot
	err := pool.QueryRow(ctx, `
		SELECT customer_id, COALESCE(customer_name,''), COALESCE(customer_contact_email,''),
		       COALESCE(customer_primary_phonenum,''), COALESCE(customer_faxnum,''),
		       COALESCE(customer_addr_line1,''), COALESCE(customer_addr_line2,''), COALESCE(customer_addr_suitenum,''),
		       COALESCE(customer_addr_city,''), customer_addr_state, customer_addr_country, COALESCE(customer_addr_zip,''),
		       customer_is_bill_as_primary,
		       COALESCE(customer_bill_addr_line1,''), COALESCE(customer_bill_addr_line2,''), COALESCE(customer_bill_addr_suitenum,''),
		       COALESCE(customer_bill_addr_city,''), customer_bill_addr_state, customer_bill_addr_country, COALESCE(customer_bill_addr_zip,'')
		FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID,
	).Scan(&id, &s.name, &s.email, &s.phone, &s.fax,
		&s.primaryLine1, &s.primaryLine2, &s.primarySuite, &s.primaryCity, &s.primaryState, &s.primaryCountry, &s.primaryZip,
		&s.billAsPrimary, &s.billLine1, &s.billLine2, &s.billSuite, &s.billCity, &s.billState, &s.billCountry, &s.billZip,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, s, ClientError{Msg: "Unknown or deleted customer."}
	}
	if err != nil {
		return 0, s, fmt.Errorf("load customer snapshot: %w", err)
	}
	return id, s, nil
}

func (s customerSnapshot) defaultBilling() Address {
	if s.billAsPrimary {
		return Address{CustomerName: s.name, Line1: s.primaryLine1, Line2: s.primaryLine2,
			SuiteNumber: s.primarySuite, City: s.primaryCity, StateID: s.primaryState, Zip: s.primaryZip,
			CountryID: s.primaryCountry, Phone: s.phone, Fax: s.fax, Email: s.email}
	}
	return Address{CustomerName: s.name, Line1: s.billLine1, Line2: s.billLine2,
		SuiteNumber: s.billSuite, City: s.billCity, StateID: s.billState, Zip: s.billZip,
		CountryID: s.billCountry, Phone: s.phone, Fax: s.fax, Email: s.email}
}

// resolveLineageInvoice resolves an optional source invoice uuid to its
// internal id, checking it belongs to the credited customer. Lineage only —
// this does NOT apply any credit (spec AD-2).
func resolveLineageInvoice(ctx context.Context, pool *pgxpool.Pool, invoiceUUID string, customerID int) (*int, error) {
	if invoiceUUID == "" {
		return nil, nil
	}
	var id, invCustomerID int
	err := pool.QueryRow(ctx,
		`SELECT invoice_id, invoice_customer_id FROM invoice WHERE invoice_uuid = $1 AND invoice_deleted_at IS NULL`,
		invoiceUUID).Scan(&id, &invCustomerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown or deleted invoice."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve source invoice: %w", err)
	}
	if invCustomerID != customerID {
		return nil, ClientError{Msg: "Invoice belongs to a different customer than the credit memo."}
	}
	return &id, nil
}

func resolveLineageSalesOrder(ctx context.Context, pool *pgxpool.Pool, salesOrderUUID string, customerID int) (*int, error) {
	if salesOrderUUID == "" {
		return nil, nil
	}
	var id, soCustomerID int
	err := pool.QueryRow(ctx,
		`SELECT sales_order_id, sales_order_customer_id FROM sales_order WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL`,
		salesOrderUUID).Scan(&id, &soCustomerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown or deleted sales order."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve source sales order: %w", err)
	}
	if soCustomerID != customerID {
		return nil, ClientError{Msg: "Sales order belongs to a different customer than the credit memo."}
	}
	return &id, nil
}

// Create inserts a new credit memo header + lines inside one transaction:
// resolves the customer's billing snapshot and optional lineage, resolves each
// line's catalog snapshot + tax, computes line/header money, validates custom
// fields, inserts header+lines, assigns the memo number post-insert, and writes
// the 'create' history row. New memos start at DRFT.
//
// Applications in the input are NOT processed here: a DRFT memo cannot move
// money (spec AD-7). Approve it, then call Apply.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateCreditMemoInput, actorEmployeeID int) (*CreditMemo, error) {
	if in.CustomerUUID == "" {
		return nil, ClientError{Msg: "customerUuid is required."}
	}
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	if len(in.Lines) == 0 {
		return nil, ClientError{Msg: "a credit memo needs at least one line."}
	}

	custID, cust, err := loadCustomerSnapshot(ctx, pool, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	srcInvoiceID, err := resolveLineageInvoice(ctx, pool, in.InvoiceUUID, custID)
	if err != nil {
		return nil, err
	}
	srcSalesOrderID, err := resolveLineageSalesOrder(ctx, pool, in.SalesOrderUUID, custID)
	if err != nil {
		return nil, err
	}

	billing := cust.defaultBilling()

	resolved := make([]resolvedLine, 0, len(in.Lines))
	lineMoney := make([]LineMoney, 0, len(in.Lines))
	for _, li := range in.Lines {
		rl, err := resolveLine(ctx, pool, li, in.SalesTaxPercent)
		if err != nil {
			return nil, err
		}
		resolved = append(resolved, rl)
		lineMoney = append(lineMoney, rl.money)
	}
	money := ComputeHeader(lineMoney, in.Adjustment, 0)

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	typeID, err := typeIDByCode(ctx, pool, "CRDT")
	if err != nil {
		return nil, err
	}
	statusID, err := statusIDByCode(ctx, pool, typeID, "DRFT")
	if err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create credit memo: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO credit_memo (
			record_type, credit_memo_status,
			credit_memo_customer_id, credit_memo_invoice_id, credit_memo_sales_order_id,
			credit_memo_reference_number, credit_memo_date, credit_memo_reason,
			credit_memo_sales_tax_percent, credit_memo_memo, credit_memo_notes, credit_memo_internal_notes,
			credit_memo_sales_rep_id, credit_memo_owner_id,
			credit_memo_price_level, credit_memo_currency,
			credit_memo_subtotal, credit_memo_discount_total, credit_memo_tax_total,
			credit_memo_adjustment, credit_memo_grand_total,
			credit_memo_applied_total, credit_memo_unapplied_amount,
			credit_memo_bill_customer_name, credit_memo_bill_attention,
			credit_memo_bill_addr_line1, credit_memo_bill_addr_line2, credit_memo_bill_addr_suitenum,
			credit_memo_bill_addr_city, credit_memo_bill_addr_state, credit_memo_bill_addr_zip,
			credit_memo_bill_addr_country, credit_memo_bill_phone, credit_memo_bill_fax, credit_memo_bill_email,
			credit_memo_custom_fields, credit_memo_created_by, credit_memo_updated_by
		) VALUES (
			$1,$2, $3,$4,$5, $6,COALESCE($7, CURRENT_DATE),$8, $9,$10,$11,$12, $13,$14, $15,$16,
			$17,$18,$19,$20,$21, $22,$23,
			$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,
			$36,$37,$38
		) RETURNING credit_memo_id, credit_memo_uuid`,
		typeID, statusID,
		custID, srcInvoiceID, srcSalesOrderID,
		in.ReferenceNumber, in.CreditMemoDate, in.Reason,
		in.SalesTaxPercent, in.Memo, in.Notes, in.InternalNotes,
		in.SalesRepID, in.OwnerEmployeeID,
		in.PriceLevelID, in.CurrencyID,
		money.Subtotal, money.DiscountTotal, money.TaxTotal,
		in.Adjustment, money.GrandTotal,
		money.AppliedTotal, money.UnappliedAmount,
		billing.CustomerName, billing.Attention,
		billing.Line1, billing.Line2, billing.SuiteNumber,
		billing.City, billing.StateID, billing.Zip,
		billing.CountryID, billing.Phone, billing.Fax, billing.Email,
		custom, nullableInt(actorEmployeeID), nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert credit memo: %w", err)
	}

	for _, rl := range resolved {
		if _, err := tx.Exec(ctx, `
			INSERT INTO credit_memo_item (
				credit_memo_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total, item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17,$18)`,
			newID, rl.in.LineNumber, rl.invItemID,
			rl.itemName, rl.sku, rl.in.Description, rl.unitID, rl.unitCode,
			rl.in.Quantity, rl.in.UnitPrice, rl.in.DiscountPercent, rl.in.TaxRateID, rl.taxPercent,
			rl.money.Subtotal, rl.money.Discount, rl.money.Tax, rl.money.Total,
			nullableInt(actorEmployeeID)); err != nil {
			return nil, fmt.Errorf("insert credit memo line: %w", err)
		}
	}

	// The document number is derived from the serial PK, so it can only be set
	// after the insert returns it. There is no sequence table.
	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx,
		`UPDATE credit_memo SET credit_memo_number = $1 WHERE credit_memo_id = $2`, number, newID); err != nil {
		return nil, fmt.Errorf("set credit memo number: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO credit_memo_history (credit_memo_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, statusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert credit memo create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create credit memo: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
