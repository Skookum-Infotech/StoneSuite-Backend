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

// shareLockLineageSalesOrder re-reads the lineage sales order inside the
// create tx under FOR SHARE, serializing against salesorder.SoftDelete's
// FOR UPDATE so a delete can't count zero credit memos while this insert is
// in flight. A nil id (no lineage) is a no-op.
func shareLockLineageSalesOrder(ctx context.Context, tx pgx.Tx, salesOrderID *int) error {
	if salesOrderID == nil {
		return nil
	}
	var id int
	err := tx.QueryRow(ctx,
		`SELECT sales_order_id FROM sales_order WHERE sales_order_id = $1 AND sales_order_deleted_at IS NULL FOR SHARE`,
		*salesOrderID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "Unknown or deleted sales order."}
	}
	if err != nil {
		return fmt.Errorf("lock source sales order: %w", err)
	}
	return nil
}
