package salesorder

import (
	"context"
	"errors"
	"fmt"
	"stonesuite-backend/historywrite"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ----- Create ---------------------------------------------------------------

// Create inserts a new sales order (header + lines) inside one transaction:
// snapshots billing/shipping from the customer (unless overridden), resolves
// and prices every line, computes header totals, assigns the order number,
// and starts the order at DRFT (spec §10, AD-4, AD-7). The work is CreateInTx;
// this wraps it with the transaction and the notification and read-back that
// only a request-driven create wants.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateOrderInput, actorEmployeeID int) (*Order, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create sales order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	created, err := CreateInTx(ctx, tx, in, actorEmployeeID, CreateOptions{})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create sales order: %w", err)
	}
	notifyCreated(ctx, pool, created.UUID, created.InternalID, actorEmployeeID)
	order, err := Get(ctx, pool, created.UUID)
	if err != nil {
		return nil, err
	}
	if created.DuplicateOfUUID != "" {
		order.DuplicateOverride = true
		order.DuplicateOfUUID = created.DuplicateOfUUID
	}
	return order, nil
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

// insertLines bulk-inserts resolved lines as sales_order_item rows and returns
// their new ids, in the order of lines.
func insertLines(ctx context.Context, tx pgx.Tx, orderInternalID int, lines []resolvedLine, actorEmployeeID int) ([]int, error) {
	ids := make([]int, 0, len(lines))
	for _, l := range lines {
		var id int
		kitUUID, kitName := l.kit.Args()
		err := tx.QueryRow(ctx, `
			INSERT INTO sales_order_item (
				sales_order_id, line_number, inventory_item_id, warehouse_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by, kit_uuid, kit_name, dim_length, dim_width, slabs, dim_unit
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19, $20,$21, $22,$23,$24,$25)
			RETURNING sales_order_item_id`,
			orderInternalID, l.lineNumber, l.inventoryItemID, l.warehouseID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID), kitUUID, kitName, l.dims.Length, l.dims.Width, l.dims.Slabs, l.dims.StoredUnit(),
		).Scan(&id)
		if err != nil {
			if isForeignKeyViolation(err) {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit, tax rate, or location was referenced.", l.lineNumber)}
			}
			if isCheckViolation(err) {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d: one or more values are out of range.", l.lineNumber)}
			}
			return nil, fmt.Errorf("insert sales order item: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// writeHistory appends a sales_order_history row. Best-effort: failures are
// logged-and-swallowed by the caller's transaction commit path (a failed
// history write should not be silently ignored inside a still-open tx, so
// this returns the error for the caller to propagate, unlike crmstore's
// fire-and-forget writeHistory which runs outside any transaction).
func writeHistory(ctx context.Context, tx pgx.Tx, orderInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	historywrite.Exec(ctx, tx, `
		INSERT INTO sales_order_history (sales_order_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		orderInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}
