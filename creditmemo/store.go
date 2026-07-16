package creditmemo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when a credit memo id matches no live row.
var ErrNotFound = errors.New("credit memo not found")

// ClientError marks a caller-fault error (maps to HTTP 400).
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// systemEmployeeID is the fallback actor for soft-delete columns that must
// never be NULL when their paired *_deleted_at timestamp is set (enforced by a
// CHECK constraint) — used when the caller has no resolvable employee id.
const systemEmployeeID = 1

// actorOrSystem returns actorEmployeeID, or systemEmployeeID if it's unset (0).
// Use this — never nullableInt — for any *_deleted_by column paired with a
// NOT NULL *_deleted_at via a CHECK constraint.
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID == 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

const headerSelect = `
	SELECT cm.credit_memo_uuid, cm.credit_memo_number,
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       c.customer_uuid, COALESCE(c.customer_name,''),
	       i.invoice_uuid, COALESCE(i.invoice_number,''),
	       so.sales_order_uuid,
	       COALESCE(ou.id::text,''), cm.credit_memo_owner_id, cm.credit_memo_sales_rep_id,
	       cm.credit_memo_reference_number, cm.credit_memo_date, cm.credit_memo_reason,
	       cm.credit_memo_sales_tax_percent,
	       cm.credit_memo_memo, cm.credit_memo_notes, cm.credit_memo_internal_notes,
	       cm.credit_memo_price_level, cm.credit_memo_currency, cm.credit_memo_exchange_rate,
	       cm.credit_memo_subtotal, cm.credit_memo_discount_total, cm.credit_memo_tax_total,
	       cm.credit_memo_adjustment, cm.credit_memo_grand_total,
	       cm.credit_memo_applied_total, cm.credit_memo_unapplied_amount,
	       cm.credit_memo_bill_customer_name, cm.credit_memo_bill_attention,
	       cm.credit_memo_bill_addr_line1, cm.credit_memo_bill_addr_line2,
	       cm.credit_memo_bill_addr_suitenum, cm.credit_memo_bill_addr_city,
	       cm.credit_memo_bill_addr_state, cm.credit_memo_bill_addr_zip,
	       cm.credit_memo_bill_addr_country, cm.credit_memo_bill_phone,
	       cm.credit_memo_bill_fax, cm.credit_memo_bill_email,
	       cm.credit_memo_custom_fields, cm.credit_memo_created_at, cm.credit_memo_updated_at,
	       cm.credit_memo_record_version,
	       cm.credit_memo_id, cm.credit_memo_status, cm.credit_memo_customer_id
	FROM credit_memo cm
	JOIN lkp_record_status rs ON rs.record_status_id = cm.credit_memo_status
	JOIN customer c ON c.customer_id = cm.credit_memo_customer_id
	LEFT JOIN invoice i ON i.invoice_id = cm.credit_memo_invoice_id
	LEFT JOIN sales_order so ON so.sales_order_id = cm.credit_memo_sales_order_id
	LEFT JOIN employee oe ON oe.employee_id = cm.credit_memo_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

type creditMemoMeta struct {
	internalID int
	statusID   int
	customerID int
}

func scanCreditMemo(row pgx.Row) (*CreditMemo, creditMemoMeta, error) {
	var (
		cm            CreditMemo
		invoiceUUID   *string
		invoiceNumber string
		salesOrderID  *string
		ownerEmpID    *int
		salesRepID    *int
		priceLevelID  *int
		currencyID    *int
		billStateID   *int
		billCountryID *int
		custom        map[string]any
		meta          creditMemoMeta
	)
	err := row.Scan(
		&cm.ID, &cm.Number,
		&cm.StatusCode, &cm.StatusName,
		&cm.Customer.ID, &cm.Customer.Name,
		&invoiceUUID, &invoiceNumber,
		&salesOrderID,
		&cm.OwnerUserID, &ownerEmpID, &salesRepID,
		&cm.ReferenceNumber, &cm.CreditMemoDate, &cm.Reason,
		&cm.SalesTaxPercent,
		&cm.Memo, &cm.Notes, &cm.InternalNotes,
		&priceLevelID, &currencyID, &cm.ExchangeRate,
		&cm.Subtotal, &cm.DiscountTotal, &cm.TaxTotal,
		&cm.Adjustment, &cm.GrandTotal,
		&cm.AppliedTotal, &cm.UnappliedAmount,
		&cm.BillingAddress.CustomerName, &cm.BillingAddress.Attention,
		&cm.BillingAddress.Line1, &cm.BillingAddress.Line2,
		&cm.BillingAddress.SuiteNumber, &cm.BillingAddress.City,
		&billStateID, &cm.BillingAddress.Zip,
		&billCountryID, &cm.BillingAddress.Phone,
		&cm.BillingAddress.Fax, &cm.BillingAddress.Email,
		&custom, &cm.CreatedAt, &cm.UpdatedAt,
		&cm.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.customerID,
	)
	if err != nil {
		return nil, creditMemoMeta{}, err
	}
	if invoiceUUID != nil {
		cm.Invoice = &InvoiceRef{ID: *invoiceUUID, Number: invoiceNumber}
	}
	cm.SalesOrderID = salesOrderID
	cm.OwnerEmployeeID = ownerEmpID
	cm.SalesRepID = salesRepID
	cm.PriceLevelID = priceLevelID
	cm.CurrencyID = currencyID
	cm.BillingAddress.StateID = billStateID
	cm.BillingAddress.CountryID = billCountryID
	// The column is NOT NULL DEFAULT '{}', but a NULL can still arrive through a
	// LEFT JOIN or an older row; never hand a nil map back to callers.
	if custom == nil {
		custom = map[string]any{}
	}
	cm.CustomFields = custom
	cm.Lines = []Line{}
	cm.Applications = []Application{}
	return &cm, meta, nil
}

const lineSelect = `
	SELECT cmi.credit_memo_item_uuid, cmi.line_number, cmi.inventory_item_id, cmi.invoice_item_id,
	       cmi.item_name, cmi.sku, cmi.description, cmi.unit_id, cmi.unit_code,
	       cmi.quantity, cmi.unit_price, cmi.discount_percent, cmi.tax_rate_id, cmi.tax_percent,
	       cmi.line_subtotal, cmi.line_discount, cmi.line_tax, cmi.line_total
	FROM credit_memo_item cmi
	WHERE cmi.credit_memo_id = $1 AND cmi.item_deleted_at IS NULL
	ORDER BY cmi.line_number ASC`

func loadLines(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Line, error) {
	rows, err := pool.Query(ctx, lineSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load credit memo lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		if err := rows.Scan(&l.ID, &l.LineNumber, &l.InventoryItemID, &l.InvoiceItemID,
			&l.ItemName, &l.SKU, &l.Description, &l.UnitID, &l.UnitCode,
			&l.Quantity, &l.UnitPrice, &l.DiscountPercent, &l.TaxRateID, &l.TaxPercent,
			&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal); err != nil {
			return nil, fmt.Errorf("scan credit memo line: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

const applicationSelect = `
	SELECT ca.application_uuid, i.invoice_uuid, COALESCE(i.invoice_number,''),
	       ca.application_amount, ca.application_created_at
	FROM credit_memo_application ca
	JOIN invoice i ON i.invoice_id = ca.invoice_id
	WHERE ca.credit_memo_id = $1 AND ca.application_deleted_at IS NULL
	ORDER BY ca.application_created_at ASC`

func loadApplications(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Application, error) {
	rows, err := pool.Query(ctx, applicationSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load credit memo applications: %w", err)
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.InvoiceID, &a.InvoiceNumber, &a.Amount, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan credit memo application: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get loads a single live credit memo (header + lines + applications) by uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*CreditMemo, error) {
	cm, meta, err := scanCreditMemo(pool.QueryRow(ctx, headerSelect+`
		WHERE cm.credit_memo_uuid = $1 AND cm.credit_memo_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get credit memo: %w", err)
	}
	lines, err := loadLines(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	cm.Lines = lines
	apps, err := loadApplications(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	cm.Applications = apps
	return cm, nil
}

func typeIDByCode(ctx context.Context, pool *pgxpool.Pool, code string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve record type %s: %w", code, err)
	}
	return id, nil
}

func statusIDByCode(ctx context.Context, pool *pgxpool.Pool, typeID int, code string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx,
		`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve status %s: %w", code, err)
	}
	return id, nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// validateCustom validates custom against the "credit_memo" workflow's field
// definitions, if one has been seeded. No-ops when it hasn't (mirrors
// invoice.validateCustom). The seeded v1 credit_memo workflow supplies these
// definitions; its states are unused by this module (spec AD-1).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "credit_memo")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load credit memo workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load credit memo field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}
