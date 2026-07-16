//go:build dbtest

package invoice_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
	"stonesuite-backend/payment"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestSoftDelete_BlockedWithLivePaymentApplication proves invoice.SoftDelete
// mirrors the guard payment.SoftDelete already enforces (spec AD-11: every
// visible payment_application row's parent must always be resolvable). An
// invoice with a live payment_application can't be deleted out from under
// it — otherwise Unapply's lockInvoiceForUpdate (payment/apply.go) would no
// longer find the invoice, stranding that application permanently.
//
// This lives in an external (_test) package rather than alongside
// invoice/store_test.go's internal tests because payment imports invoice; an
// internal test (package invoice) importing payment back would be a
// same-archive import cycle, while an external test package (invoice_test)
// is its own compilation unit and isn't.
func TestSoftDelete_BlockedWithLivePaymentApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	var custUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`, custTypeID, "Test Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, 'Test Item', 1, 100, 1) RETURNING inventory_item_uuid`, "TEST-SKU-"+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}

	inv, err := invoice.Create(ctx, pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 100}},
	}, 1)
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	for _, st := range []string{"PAPV", "APPV", "SENT"} {
		if inv, err = invoice.Transition(ctx, pool, inv.ID, st, 1); err != nil {
			t.Fatalf("transition invoice to %s: %v", st, err)
		}
	}

	var methodID int
	if err := pool.QueryRow(ctx,
		`SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'CHK_'`).Scan(&methodID); err != nil {
		t.Fatalf("resolve payment method: %v", err)
	}

	p, err := payment.Create(ctx, pool, payment.CreatePaymentInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: 100,
		Applications: []payment.ApplicationInput{{InvoiceUUID: inv.ID, Amount: 100}},
	}, 1)
	if err != nil {
		t.Fatalf("create payment with inline application: %v", err)
	}

	// The invoice now has a live payment_application against it. Deleting it
	// here would strand that application — Unapply could no longer resolve
	// the invoice, and the payment itself can't be deleted either while it
	// still has a live application. Expect a ClientError, not success.
	if err := invoice.SoftDelete(ctx, pool, inv.ID, 1); err == nil {
		t.Fatal("expected error deleting an invoice with a live payment application")
	} else if _, ok := err.(invoice.ClientError); !ok {
		t.Fatalf("expected invoice.ClientError, got %T: %v", err, err)
	}

	// Unapply the payment, freeing the invoice; SoftDelete must now succeed.
	if _, err := payment.Unapply(ctx, pool, p.ID, inv.ID, 1); err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if err := invoice.SoftDelete(ctx, pool, inv.ID, 1); err != nil {
		t.Fatalf("expected soft delete to succeed after unapply, got %v", err)
	}
}
