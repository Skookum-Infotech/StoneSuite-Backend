//go:build dbtest

package invoice

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// seedCustomerAndItem inserts a minimal live customer + inventory_item.
func seedCustomerAndItem(t *testing.T, pool *pgxpool.Pool) (custUUID, itemUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`, custTypeID, "Test Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, 'Test Item', 1, 42.00, 1) RETURNING inventory_item_uuid`, "TEST-SKU-"+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	return custUUID, itemUUID
}

func TestCreate_SnapshotsAndTotals(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateInvoiceInput{
		CustomerUUID:    custUUID,
		SalesTaxPercent: 8.25,
		Items:           []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 10, UnitPrice: 100, DiscountPercent: 5}},
	}
	inv, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(inv.Number, "INVC-") {
		t.Fatalf("expected INVC-prefixed number, got %q", inv.Number)
	}
	if inv.StatusCode != "DRFT" {
		t.Fatalf("new invoice must start DRFT, got %s", inv.StatusCode)
	}
	if len(inv.Items) != 1 || inv.Items[0].SKU == "" || inv.Items[0].ItemName == "" {
		t.Fatalf("line item snapshot (sku/name) not populated: %+v", inv.Items)
	}
	// (1000 - 50) * 8.25% = 78.375 -> 78.38; grand total = 1000 - 50 + 78.38 = 1028.38
	if inv.GrandTotal != 1028.38 {
		t.Fatalf("grand total = %v, want 1028.38", inv.GrandTotal)
	}
	if inv.BalanceDue != 1028.38 {
		t.Fatalf("balance due = %v, want 1028.38", inv.BalanceDue)
	}
}

func TestGetUpdateSoftDelete(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	inv, err := Create(ctx, pool, CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := Get(ctx, pool, inv.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("expected 1 line, got %d", len(got.Items))
	}

	updated, err := Update(ctx, pool, inv.ID, UpdateInvoiceInput{
		SalesTaxPercent: 0,
		Items:           []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.GrandTotal != 20 {
		t.Fatalf("expected recomputed grand total 20, got %v", updated.GrandTotal)
	}

	if err := SoftDelete(ctx, pool, inv.ID, 1); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if _, err := Get(ctx, pool, inv.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after soft delete, got %v", err)
	}
}

func TestUpdate_RejectedOnTerminalStatus(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	inv, err := Create(ctx, pool, CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Note: We need a Transition function to move to PAID, but since we don't have it yet in tests, we can manually update the DB status for testing Update rejection, or wait until Task 3.3.
	// For now, let's just manually update the status to PAID to test the rejection.
	var typeID int
	if err := pool.QueryRow(ctx, "SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'INVC'").Scan(&typeID); err != nil {
		t.Fatalf("get type: %v", err)
	}
	var paidStatusID int
	if err := pool.QueryRow(ctx, "SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAID'", typeID).Scan(&paidStatusID); err != nil {
		t.Fatalf("get status: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE invoice SET invoice_status = $1 WHERE invoice_uuid = $2", paidStatusID, inv.ID); err != nil {
		t.Fatalf("force status: %v", err)
	}

	if _, err := Update(ctx, pool, inv.ID, UpdateInvoiceInput{Items: []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 5, UnitPrice: 10}}}, 1); err == nil {
		t.Fatal("expected update on PAID invoice to be rejected")
	}
}

// C2: Update soft-deletes prior lines (keeps history) and re-inserts the same
// line_number via the partial unique index.
func TestUpdate_SoftDeletesLines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	inv, err := Create(ctx, pool, CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Update(ctx, pool, inv.ID, UpdateInvoiceInput{
		Items: []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2, UnitPrice: 10}},
	}, 1); err != nil {
		t.Fatalf("update: %v", err)
	}

	var internalID int
	if err := pool.QueryRow(ctx, `SELECT invoice_id FROM invoice WHERE invoice_uuid = $1`, inv.ID).Scan(&internalID); err != nil {
		t.Fatalf("resolve id: %v", err)
	}
	var total, live int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*), COUNT(*) FILTER (WHERE item_deleted_at IS NULL) FROM invoice_item WHERE invoice_id = $1`, internalID).Scan(&total, &live); err != nil {
		t.Fatalf("count lines: %v", err)
	}
	if live != 1 {
		t.Fatalf("expected 1 live line after update, got %d", live)
	}
	if total != 2 {
		t.Fatalf("expected prior line soft-deleted (2 rows total), got %d — history was hard-deleted", total)
	}
}

// C3: Update cannot reduce the total below the amount already paid.
func TestUpdate_RejectsBelowAmountPaid(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	inv, err := Create(ctx, pool, CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 100}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, st := range []string{"PAPV", "APPV", "SENT"} {
		if inv, err = Transition(ctx, pool, inv.ID, st, 1); err != nil {
			t.Fatalf("transition to %s: %v", st, err)
		}
	}
	// amount_paid is now written by the payment module, not this package —
	// seed it directly to test Update's own guard in isolation.
	if _, err := pool.Exec(ctx, `UPDATE invoice SET invoice_amount_paid = 60 WHERE invoice_uuid = $1`, inv.ID); err != nil {
		t.Fatalf("seed amount_paid: %v", err)
	}
	// Reduce the total to 10, below the 60 already paid → ClientError, not a 500.
	_, err = Update(ctx, pool, inv.ID, UpdateInvoiceInput{
		Items: []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if _, ok := err.(ClientError); !ok {
		t.Fatalf("expected ClientError reducing total below amount paid, got %T: %v", err, err)
	}
}

func TestCreate_UnknownCustomer_IsClientError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, err := Create(ctx, pool, CreateInvoiceInput{CustomerUUID: "00000000-0000-0000-0000-000000000000"}, 1)
	if err == nil {
		t.Fatal("expected error for unknown customer")
	}
	if _, ok := err.(ClientError); !ok {
		t.Fatalf("expected ClientError, got %T: %v", err, err)
	}
}
