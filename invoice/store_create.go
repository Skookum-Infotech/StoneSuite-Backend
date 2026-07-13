package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// customerSnapshot is the subset of customer columns needed to default the
// billing/shipping snapshot at invoice creation.
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
	shipAsPrimary                             bool
	shipLine1, shipLine2, shipSuite, shipCity string
	shipState, shipCountry                    *int
	shipZip                                   string
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
		       COALESCE(customer_bill_addr_city,''), customer_bill_addr_state, customer_bill_addr_country, COALESCE(customer_bill_addr_zip,''),
		       customer_is_ship_as_primary,
		       COALESCE(customer_ship_addr_line1,''), COALESCE(customer_ship_addr_line2,''), COALESCE(customer_ship_addr_suitenum,''),
		       COALESCE(customer_ship_addr_city,''), customer_ship_addr_state, customer_ship_addr_country, COALESCE(customer_ship_addr_zip,'')
		FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID,
	).Scan(&id, &s.name, &s.email, &s.phone, &s.fax,
		&s.primaryLine1, &s.primaryLine2, &s.primarySuite, &s.primaryCity, &s.primaryState, &s.primaryCountry, &s.primaryZip,
		&s.billAsPrimary, &s.billLine1, &s.billLine2, &s.billSuite, &s.billCity, &s.billState, &s.billCountry, &s.billZip,
		&s.shipAsPrimary, &s.shipLine1, &s.shipLine2, &s.shipSuite, &s.shipCity, &s.shipState, &s.shipCountry, &s.shipZip,
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
		return Address{CustomerName: s.name, AddrLine1: s.primaryLine1, AddrLine2: s.primaryLine2,
			SuiteUnit: s.primarySuite, City: s.primaryCity, StateID: s.primaryState, Zip: s.primaryZip,
			CountryID: s.primaryCountry, Phone: s.phone, Fax: s.fax, Email: s.email}
	}
	return Address{CustomerName: s.name, AddrLine1: s.billLine1, AddrLine2: s.billLine2,
		SuiteUnit: s.billSuite, City: s.billCity, StateID: s.billState, Zip: s.billZip,
		CountryID: s.billCountry, Phone: s.phone, Fax: s.fax, Email: s.email}
}

func (s customerSnapshot) defaultShipping() Address {
	if s.shipAsPrimary {
		return Address{CustomerName: s.name, AddrLine1: s.primaryLine1, AddrLine2: s.primaryLine2,
			SuiteUnit: s.primarySuite, City: s.primaryCity, StateID: s.primaryState, Zip: s.primaryZip,
			CountryID: s.primaryCountry, Phone: s.phone, Fax: s.fax, Email: s.email}
	}
	return Address{CustomerName: s.name, AddrLine1: s.shipLine1, AddrLine2: s.shipLine2,
		SuiteUnit: s.shipSuite, City: s.shipCity, StateID: s.shipState, Zip: s.shipZip,
		CountryID: s.shipCountry, Phone: s.phone, Fax: s.fax, Email: s.email}
}

func isZeroAddress(a Address) bool {
	return a.CustomerName == "" && a.AddrLine1 == "" && a.City == "" && a.Zip == "" && a.Email == ""
}

// Create inserts a new invoice header + lines inside one transaction:
// resolves the customer's billing/shipping snapshot, resolves each line's catalog snapshot + tax,
// computes line/header money, validates custom fields, inserts header+lines,
// assigns the invoice number post-insert, and writes the 'create' history row.
// New invoices start at DRFT.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateInvoiceInput, actorEmployeeID int) (*Invoice, error) {
	if in.CustomerUUID == "" {
		return nil, ClientError{Msg: "customerUuid is required."}
	}
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	custID, cust, err := loadCustomerSnapshot(ctx, pool, in.CustomerUUID)
	if err != nil {
		return nil, err
	}

	billing := cust.defaultBilling()
	if !isZeroAddress(in.Billing) {
		billing = in.Billing
	}
	shipping := cust.defaultShipping()
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
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment, 0) // amountPaid starts at 0

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	typeID, err := typeIDByCode(ctx, pool, "INVC")
	if err != nil {
		return nil, err
	}
	draftStatusID, err := statusIDByCode(ctx, pool, typeID, "DRFT")
	if err != nil {
		return nil, err
	}

	ownerEmp := in.OwnerEmployeeID
	if ownerEmp == nil && actorEmployeeID != 0 {
		ownerEmp = &actorEmployeeID
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create invoice: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO invoice (
			record_type, invoice_status, invoice_customer_id,
			invoice_po_number, invoice_reference_number, invoice_date, invoice_due_date,
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
			$1,$2,$3, $4,$5,COALESCE($6, CURRENT_DATE),$7, $8,$9,$10,$11,$12, $13,$14, $15,$16,$17,
			$18,$19,$20, $21,$22,$23,
			$24,$25,
			$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,$36,$37,
			$38,$39,$40,$41,$42,$43,$44,$45,$46,$47,$48,$49,$50,
			$51,$52,$52
		) RETURNING invoice_id, invoice_uuid`,
		typeID, draftStatusID, custID,
		in.PONumber, in.ReferenceNumber, in.InvoiceDate, in.DueDate,
		in.SalesTaxPercent, in.Memo, in.Notes, in.InternalNotes, in.TermsConditions,
		in.SalesRepEmployeeID, ownerEmp,
		in.PaymentTermsID, in.PriceLevelID, in.CurrencyID,
		header.Subtotal, header.DiscountTotal, header.TaxTotal,
		in.ShippingCharge, in.Adjustment, header.GrandTotal,
		header.AmountPaid, header.BalanceDue,
		billing.CustomerName, billing.Attention, billing.AddrLine1, billing.AddrLine2,
		billing.SuiteUnit, billing.City, billing.StateID, billing.Zip,
		billing.CountryID, billing.Phone, billing.Fax, billing.Email,
		in.ShipSameAsBilling, shipping.CustomerName, shipping.Attention,
		shipping.AddrLine1, shipping.AddrLine2, shipping.SuiteUnit, shipping.City,
		shipping.StateID, shipping.Zip, shipping.CountryID,
		shipping.Phone, shipping.Fax, shipping.Email,
		custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert invoice: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE invoice SET invoice_number = $1 WHERE invoice_id = $2`, number, newID); err != nil {
		return nil, fmt.Errorf("set invoice number: %w", err)
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
			newID, rl.in.LineNumber, rl.invItemID,
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
		VALUES ($1, NULL, $2, 'create', $3)`, newID, draftStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert invoice create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create invoice: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
