//go:build dbtest

package payment

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
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

// seedCustomer inserts a minimal live customer.
func seedCustomer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
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
	return custUUID
}

// seedSentInvoice creates a customer + a 100.00 invoice already transitioned
// to SENT (the only status Apply accepts payment against).
func seedSentInvoice(t *testing.T, pool *pgxpool.Pool, amount float64) (custUUID, invUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, 'Test Item', 1, $2, 1) RETURNING inventory_item_uuid`, "TEST-SKU-"+suffix, amount).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	custUUID = seedCustomer(t, pool)
	inv, err := invoice.Create(ctx, pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: amount}},
	}, 1)
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	for _, st := range []string{"PAPV", "APPV", "SENT"} {
		if inv, err = invoice.Transition(ctx, pool, inv.ID, st, 1); err != nil {
			t.Fatalf("transition invoice to %s: %v", st, err)
		}
	}
	return custUUID, inv.ID
}

func firstMethodID(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(),
		`SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'CHK_'`).Scan(&id); err != nil {
		t.Fatalf("resolve payment method: %v", err)
	}
	return id
}

func TestCreate_HeaderOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)

	p, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: 500,
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(p.Number, "PYMT-") {
		t.Fatalf("expected PYMT- prefixed number, got %q", p.Number)
	}
	if p.StatusCode != "PEND" {
		t.Fatalf("new payment must start PEND, got %s", p.StatusCode)
	}
	if p.AppliedTotal != 0 || p.UnappliedAmount != 500 {
		t.Fatalf("expected 0 applied / 500 unapplied, got applied=%v unapplied=%v", p.AppliedTotal, p.UnappliedAmount)
	}
}

func TestCreate_WithInlineApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)

	p, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: 150,
		Applications: []ApplicationInput{{InvoiceUUID: invUUID, Amount: 100}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.AppliedTotal != 100 || p.UnappliedAmount != 50 {
		t.Fatalf("expected applied=100 unapplied=50, got applied=%v unapplied=%v", p.AppliedTotal, p.UnappliedAmount)
	}
	if len(p.Applications) != 1 || p.Applications[0].InvoiceID != invUUID {
		t.Fatalf("expected 1 application against %s, got %+v", invUUID, p.Applications)
	}
	inv, err := invoice.Get(ctx, pool, invUUID)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	if inv.AmountPaid != 100 || inv.BalanceDue != 0 || inv.StatusCode != "PAID" {
		t.Fatalf("expected invoice fully paid, got paid=%v balance=%v status=%s", inv.AmountPaid, inv.BalanceDue, inv.StatusCode)
	}
}

func TestCreate_UnknownCustomer_IsClientError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	methodID := firstMethodID(t, pool)
	_, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: "00000000-0000-0000-0000-000000000000", MethodID: methodID, Amount: 10,
	}, 1)
	if _, ok := err.(ClientError); !ok {
		t.Fatalf("expected ClientError, got %T: %v", err, err)
	}
}

func TestApply_CapsAtInvoiceBalance(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 200}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 150, 1); err == nil {
		t.Fatal("expected error applying more than the invoice's balance_due (100)")
	}
	p2, err := Apply(ctx, pool, p.ID, invUUID, 100, 1)
	if err != nil {
		t.Fatalf("apply within balance: %v", err)
	}
	if p2.AppliedTotal != 100 || p2.UnappliedAmount != 100 {
		t.Fatalf("applied=%v unapplied=%v, want 100/100", p2.AppliedTotal, p2.UnappliedAmount)
	}
}

func TestApply_RejectsCrossCustomerInvoice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, invUUID := seedSentInvoice(t, pool, 100) // invoice belongs to its own customer
	otherCust := seedCustomer(t, pool)          // payment belongs to a different customer
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: otherCust, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 50, 1); err == nil {
		t.Fatal("expected error applying to an invoice of a different customer")
	}
}

func TestApply_RejectsUnsentInvoice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	// Invoice left at DRFT (never transitioned to SENT).
	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ('DRFT-SKU', 'Item', 1, 50, 1) RETURNING inventory_item_uuid`).Scan(&itemUUID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	inv, err := invoice.Create(ctx, pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID, Items: []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 50}},
	}, 1)
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 50}, 1)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, inv.ID, 50, 1); err == nil {
		t.Fatal("expected error applying to a DRFT invoice")
	}
}

func TestUnapply_RestoresBalanceAndRevertsStatus(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 100, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	inv, _ := invoice.Get(ctx, pool, invUUID)
	if inv.StatusCode != "PAID" {
		t.Fatalf("expected PAID after full apply, got %s", inv.StatusCode)
	}

	p2, err := Unapply(ctx, pool, p.ID, invUUID, 1)
	if err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if p2.AppliedTotal != 0 || p2.UnappliedAmount != 100 {
		t.Fatalf("applied=%v unapplied=%v, want 0/100", p2.AppliedTotal, p2.UnappliedAmount)
	}
	inv2, _ := invoice.Get(ctx, pool, invUUID)
	if inv2.AmountPaid != 0 || inv2.BalanceDue != 100 || inv2.StatusCode != "SENT" {
		t.Fatalf("expected invoice reverted to SENT/0/100, got status=%s paid=%v balance=%v", inv2.StatusCode, inv2.AmountPaid, inv2.BalanceDue)
	}
}

func TestApply_ReapplyIncreasesExistingRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 40, 1); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	p2, err := Apply(ctx, pool, p.ID, invUUID, 60, 1)
	if err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	if len(p2.Applications) != 1 {
		t.Fatalf("expected exactly 1 live application row (merged), got %d", len(p2.Applications))
	}
	if p2.Applications[0].Amount != 100 {
		t.Fatalf("expected merged application amount 100, got %v", p2.Applications[0].Amount)
	}
}
