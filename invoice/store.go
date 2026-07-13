package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when an invoice id matches no live row.
var ErrNotFound = errors.New("invoice not found")

// ClientError marks a caller-fault error (maps to HTTP 400).
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// terminalStatuses are the statuses Update/Transition reject certain edits against.
var terminalStatuses = map[string]bool{"PAID": true, "VOID": true}

const headerSelect = `
	SELECT i.invoice_uuid, i.invoice_number,
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       c.customer_uuid, COALESCE(c.customer_name,''),
	       COALESCE(so.sales_order_uuid::text,''), COALESCE(so.sales_order_number,''),
	       COALESCE(ou.id::text,''), i.invoice_owner_id, i.invoice_sales_rep_id,
	       i.invoice_po_number, i.invoice_reference_number,
	       i.invoice_date, i.invoice_due_date,
	       i.invoice_payment_terms, i.invoice_price_level, i.invoice_currency,
	       i.invoice_exchange_rate, i.invoice_sales_tax_percent,
	       i.invoice_memo, i.invoice_notes, i.invoice_internal_notes, i.invoice_terms_conditions,
	       i.invoice_subtotal, i.invoice_discount_total, i.invoice_tax_total,
	       i.invoice_shipping_charge, i.invoice_adjustment, i.invoice_grand_total,
	       i.invoice_amount_paid, i.invoice_balance_due,
	       i.invoice_ship_same_as_bill,
	       i.invoice_bill_customer_name, i.invoice_bill_attention,
	       i.invoice_bill_addr_line1, i.invoice_bill_addr_line2, i.invoice_bill_addr_suitenum,
	       i.invoice_bill_addr_city, i.invoice_bill_addr_state, i.invoice_bill_addr_zip,
	       i.invoice_bill_addr_country, i.invoice_bill_phone, i.invoice_bill_fax, i.invoice_bill_email,
	       i.invoice_ship_customer_name, i.invoice_ship_attention,
	       i.invoice_ship_addr_line1, i.invoice_ship_addr_line2, i.invoice_ship_addr_suitenum,
	       i.invoice_ship_addr_city, i.invoice_ship_addr_state, i.invoice_ship_addr_zip,
	       i.invoice_ship_addr_country, i.invoice_ship_phone, i.invoice_ship_fax, i.invoice_ship_email,
	       i.invoice_custom_fields, i.invoice_created_at, i.invoice_updated_at, i.invoice_record_version,
	       i.invoice_id, i.invoice_status, i.invoice_customer_id
	FROM invoice i
	JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
	JOIN customer c ON c.customer_id = i.invoice_customer_id
	LEFT JOIN sales_order so ON so.sales_order_id = i.invoice_sales_order_id
	LEFT JOIN employee oe ON oe.employee_id = i.invoice_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

type invoiceMeta struct {
	internalID int
	statusID   int
	customerID int
}

func scanInvoice(row pgx.Row) (*Invoice, invoiceMeta, error) {
	var (
		i                          Invoice
		ownerEmpID, salesRepID     *int
		paymentTermsID, priceLvlID *int
		currencyID                 *int
		billState, billCountry     *int
		shipState, shipCountry     *int
		custom                     map[string]any
		meta                       invoiceMeta
		soUUID, soNum              string
	)
	err := row.Scan(
		&i.ID, &i.Number,
		&i.StatusCode, &i.StatusName,
		&i.Customer.ID, &i.Customer.Name,
		&soUUID, &soNum,
		&i.OwnerUserID, &ownerEmpID, &salesRepID,
		&i.PONumber, &i.ReferenceNumber,
		&i.InvoiceDate, &i.DueDate,
		&paymentTermsID, &priceLvlID, &currencyID,
		&i.ExchangeRate, &i.SalesTaxPercent,
		&i.Memo, &i.Notes, &i.InternalNotes, &i.TermsConditions,
		&i.Subtotal, &i.DiscountTotal, &i.TaxTotal,
		&i.ShippingCharge, &i.Adjustment, &i.GrandTotal,
		&i.AmountPaid, &i.BalanceDue,
		&i.ShipSameAsBilling,
		&i.Billing.CustomerName, &i.Billing.Attention,
		&i.Billing.AddrLine1, &i.Billing.AddrLine2, &i.Billing.SuiteUnit,
		&i.Billing.City, &billState, &i.Billing.Zip,
		&billCountry, &i.Billing.Phone, &i.Billing.Fax, &i.Billing.Email,
		&i.Shipping.CustomerName, &i.Shipping.Attention,
		&i.Shipping.AddrLine1, &i.Shipping.AddrLine2, &i.Shipping.SuiteUnit,
		&i.Shipping.City, &shipState, &i.Shipping.Zip,
		&shipCountry, &i.Shipping.Phone, &i.Shipping.Fax, &i.Shipping.Email,
		&custom, &i.CreatedAt, &i.UpdatedAt, &i.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.customerID,
	)
	if err != nil {
		return nil, invoiceMeta{}, err
	}
	if soUUID != "" {
		i.SalesOrder = &SalesOrderRef{ID: soUUID, Number: soNum}
	}
	i.OwnerEmployeeID = ownerEmpID
	i.SalesRepEmployeeID = salesRepID
	i.PaymentTermsID = paymentTermsID
	i.PriceLevelID = priceLvlID
	i.CurrencyID = currencyID
	i.Billing.StateID = billState
	i.Billing.CountryID = billCountry
	i.Shipping.StateID = shipState
	i.Shipping.CountryID = shipCountry
	if custom == nil {
		custom = map[string]any{}
	}
	i.CustomFields = custom
	i.Items = []Line{}
	return &i, meta, nil
}

const lineSelect = `
	SELECT ii.invoice_item_uuid, ii.line_number, COALESCE(inv.inventory_item_uuid::text,''), COALESCE(soi.sales_order_item_uuid::text,''),
	       ii.sku, ii.item_name, ii.description, ii.unit_id, ii.unit_code,
	       ii.quantity, ii.unit_price, ii.discount_percent, ii.tax_rate_id, ii.tax_percent,
	       ii.line_subtotal, ii.line_discount, ii.line_tax, ii.line_total
	FROM invoice_item ii
	LEFT JOIN inventory_item inv ON inv.inventory_item_id = ii.inventory_item_id
	LEFT JOIN sales_order_item soi ON soi.sales_order_item_id = ii.sales_order_item_id
	WHERE ii.invoice_id = $1 AND ii.item_deleted_at IS NULL
	ORDER BY ii.line_number ASC`

func loadLines(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Line, error) {
	rows, err := pool.Query(ctx, lineSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load invoice lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		var invItemUUID, soItemUUID string
		if err := rows.Scan(&l.ID, &l.LineNumber, &invItemUUID, &soItemUUID,
			&l.SKU, &l.ItemName, &l.Description, &l.UnitID, &l.UnitCode,
			&l.Quantity, &l.UnitPrice, &l.DiscountPercent, &l.TaxRateID, &l.TaxPercent,
			&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal); err != nil {
			return nil, fmt.Errorf("scan invoice line: %w", err)
		}
		if invItemUUID != "" {
			l.InventoryItemID = &invItemUUID
		}
		if soItemUUID != "" {
			l.SalesOrderItemID = &soItemUUID
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get loads a single live invoice (header + lines) by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*Invoice, error) {
	inv, meta, err := scanInvoice(pool.QueryRow(ctx, headerSelect+`
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get invoice: %w", err)
	}
	lines, err := loadLines(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	inv.Items = lines
	return inv, nil
}

// typeIDByCode resolves an lkp_record_type row by its code.
func typeIDByCode(ctx context.Context, pool *pgxpool.Pool, code string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve record type %s: %w", code, err)
	}
	return id, nil
}

// statusIDByCode resolves an lkp_record_status row for a record type + code.
func statusIDByCode(ctx context.Context, pool *pgxpool.Pool, typeID int, code string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx,
		`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve status %s: %w", code, err)
	}
	return id, nil
}

// statusCodeByID resolves a status row's code from its internal id.
func statusCodeByID(ctx context.Context, pool *pgxpool.Pool, statusID int) (string, error) {
	var code string
	if err := pool.QueryRow(ctx,
		`SELECT record_status_code FROM lkp_record_status WHERE record_status_id = $1`, statusID).Scan(&code); err != nil {
		return "", fmt.Errorf("resolve status code: %w", err)
	}
	return code, nil
}

// validateCustom validates in.CustomFields against the "invoice" workflow's field definitions.
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "invoice")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load invoice workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load invoice field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
