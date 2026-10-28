package creditmemo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/idempotency"
	"stonesuite-backend/workflow"
)

// Create inserts a new credit memo header + lines inside one transaction:
// resolves the customer's billing snapshot and optional lineage, resolves each
// line's catalog snapshot + tax, computes line/header money, validates custom
// fields, inserts header+lines, assigns the memo number post-insert, and writes
// the 'create' history row. New memos start at DRFT.
//
// The money comes from Amount (no lines) or from Lines. With SourcePaymentUUID
// the memo's total is taken out of that payment's overpayment in this same
// transaction, so the same money is never credited twice.
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
	// A retry whose first attempt already committed gets that credit memo back
	// before any validation: the customer may have changed since, and a
	// replay must answer like the original, not re-judge it.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Lookup(ctx, pool, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			return Get(ctx, pool, prior)
		}
	}
	if err := validateWriteInput(len(in.Lines), in.Amount, in.Adjustment, in.CreditMemoDate); err != nil {
		return nil, err
	}
	if err := validateCreateMoney(in.Amount, in.Lines); err != nil {
		return nil, err
	}

	msg, err := workflow.CustomerNotUsableByUUID(ctx, pool, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	if msg != "" {
		return nil, ClientError{Msg: msg}
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
	if len(in.Lines) == 0 {
		// No line items: the amount stands in for them (subtotal = amount).
		lineMoney = append(lineMoney, ComputeAmountLine(in.Amount, in.SalesTaxPercent))
	}
	money := ComputeHeader(lineMoney, in.Adjustment, 0)
	if money.GrandTotal < 0 {
		return nil, ClientError{Msg: "The credit memo total cannot be negative."}
	}

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

	// Default the owner to the creating employee when unset, matching every
	// sibling document module (see payment.Create) -- without this,
	// credit_memo_owner_id stays NULL and an "own"-scoped caller can never see
	// a memo they just created (creditmemo.Search's scope predicate is an
	// equality match, which never matches NULL).
	ownerEmp := in.OwnerEmployeeID
	if ownerEmp == nil && actorEmployeeID != 0 {
		ownerEmp = &actorEmployeeID
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create credit memo: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Claim inside the create's transaction: a concurrent duplicate blocks
	// here until this one commits, then replays it instead of creating.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Claim(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			_ = tx.Rollback(ctx)
			return Get(ctx, pool, prior)
		}
	}

	if err := shareLockLineageSalesOrder(ctx, tx, srcSalesOrderID); err != nil {
		return nil, err
	}

	// Lock the source payment and check it can cover the memo before inserting:
	// the check and the insert then see the same overpayment.
	var sourcePaymentID *int
	if in.SourcePaymentUUID != "" {
		paymentID, room, err := lockSourcePayment(ctx, tx, in.SourcePaymentUUID, custID)
		if err != nil {
			return nil, err
		}
		if err := checkPaymentRoom(money.GrandTotal, room); err != nil {
			return nil, err
		}
		sourcePaymentID = &paymentID
	}

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
			credit_memo_custom_fields, credit_memo_created_by, credit_memo_updated_by,
			credit_memo_source_payment_id
		) VALUES (
			$1,$2, $3,$4,$5, $6,COALESCE(NULLIF($7,'')::date, CURRENT_DATE),$8, $9,$10,$11,$12, $13,$14, $15,$16,
			$17,$18,$19,$20,$21, $22,$23,
			$24,$25,$26,$27,$28,$29,$30,$31,$32,$33,$34,$35,
			$36,$37,$38,$39
		) RETURNING credit_memo_id, credit_memo_uuid`,
		typeID, statusID,
		custID, srcInvoiceID, srcSalesOrderID,
		in.ReferenceNumber, in.CreditMemoDate, in.Reason,
		in.SalesTaxPercent, in.Memo, in.Notes, in.InternalNotes,
		in.SalesRepID, ownerEmp,
		in.PriceLevelID, in.CurrencyID,
		money.Subtotal, money.DiscountTotal, money.TaxTotal,
		in.Adjustment, money.GrandTotal,
		money.AppliedTotal, money.UnappliedAmount,
		billing.CustomerName, billing.Attention,
		billing.Line1, billing.Line2, billing.SuiteNumber,
		billing.City, billing.StateID, billing.Zip,
		billing.CountryID, billing.Phone, billing.Fax, billing.Email,
		custom, nullableInt(actorEmployeeID), nullableInt(actorEmployeeID),
		sourcePaymentID,
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

	if sourcePaymentID != nil {
		if err := recomputePaymentCredited(ctx, tx, *sourcePaymentID, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	// The document number can only be assigned after the insert returns the
	// serial PK (the FormatNumber fallback needs it, and there is no sequence
	// table). assignNumber honors the "credit_memo" Record Numbering config
	// when one is enabled.
	if _, err := assignNumber(ctx, tx, int64(newID)); err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" {
		if err := idempotency.Complete(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, newUUID); err != nil {
			return nil, err
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO credit_memo_history (credit_memo_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, statusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert credit memo create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create credit memo: %w", err)
	}
	notifyCreated(ctx, pool, newUUID, newID, actorEmployeeID)
	return Get(ctx, pool, newUUID)
}
