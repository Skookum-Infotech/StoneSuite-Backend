// quote/store_test.go
//go:build dbtest

package quote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/query"
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

// seedCustomerAndItem inserts a minimal live customer + inventory_item,
// mirroring invoice/store_test.go's helper of the same name.
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
		VALUES ($1, $2, 1) RETURNING customer_uuid`,
		custTypeID, "Test Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 25.00, 1) RETURNING inventory_item_uuid`,
		"SKU-"+suffix, "Test Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	return custUUID, itemUUID
}

func TestCreate_SnapshotsAndTotals(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields: quoteFields{
			SalesTaxPercent: 8,
			Items: []LineInput{
				{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2, DiscountPercent: 0},
			},
		},
	}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Number == "" || got.Number[:5] != "QUOT-" {
		t.Errorf("Number = %q, want QUOT- prefix", got.Number)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	// unit price snapshotted from inventory_item (25.00) since request left UnitPrice at 0.
	if got.Items[0].UnitPrice != 25 {
		t.Errorf("Items[0].UnitPrice = %v, want 25", got.Items[0].UnitPrice)
	}
	// subtotal = 2*25=50, tax = 50*0.08=4, grand = 54
	if got.GrandTotal != 54 {
		t.Errorf("GrandTotal = %v, want 54", got.GrandTotal)
	}
}

func TestCreate_RequiresCustomer(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateQuoteInput{}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with no customer = %v, want ClientError", err)
	}
}

func TestUpdate_RecomputesTotalsAndBumpsVersion(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields: quoteFields{
			SalesTaxPercent: 0,
			Items:           []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := Update(context.Background(), pool, created.ID, UpdateQuoteInput{
		quoteFields: quoteFields{
			SalesTaxPercent: 0,
			Items:           []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 3}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 3 * 25 = 75
	if updated.GrandTotal != 75 {
		t.Errorf("GrandTotal after update = %v, want 75", updated.GrandTotal)
	}
	if len(updated.Items) != 1 || updated.Items[0].Quantity != 3 {
		t.Fatalf("Items after update = %+v, want single line qty 3", updated.Items)
	}
}

func TestSoftDelete_ThenGetReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID:   custUUID,
		quoteFields: quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SoftDelete(context.Background(), pool, created.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := Get(context.Background(), pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestTransition_DraftToPendingApproval(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID:   custUUID,
		quoteFields: quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := Transition(context.Background(), pool, created.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if updated.StatusCode != "PAPV" {
		t.Errorf("StatusCode = %q, want PAPV", updated.StatusCode)
	}
}

func TestTransition_RejectsIllegalMove(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID:   custUUID,
		quoteFields: quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "APPV", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition DRFT->APPV = %v, want ErrInvalidTransition", err)
	}
}

func TestApprove_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID:   custUUID,
		quoteFields: quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	// No quote_approver rows configured for (QUOT, PAPV) in this test DB by
	// default, so Approve should report the status doesn't require approval.
	if _, err := Approve(context.Background(), pool, created.ID, 1); !errors.Is(err, ErrApprovalNotRequired) {
		t.Fatalf("Approve with no configured approvers = %v, want ErrApprovalNotRequired", err)
	}
}

func TestApprove_SignOffFlipsApprovalStatus(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateQuoteInput{
		CustomerUUID:   custUUID,
		quoteFields: quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}

	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'QUOT'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve QUOT record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO quote_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed quote_approver: %v", err)
	}

	approved, err := Approve(ctx, pool, created.ID, 1)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.ApprovalStatus != "approved" {
		t.Errorf("ApprovalStatus = %q, want approved", approved.ApprovalStatus)
	}
}

func TestSearch_ReturnsCreatedQuote(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateQuoteInput{
		CustomerUUID:   custUUID,
		quoteFields: quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	page, err := Search(ctx, pool, "all", "", query.Request{Search: created.Number})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range page.Records {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(%q) did not include the created quote", created.Number)
	}
}
