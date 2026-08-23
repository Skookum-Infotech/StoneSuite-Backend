//go:build dbtest

package workflow_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/estimate"
	"stonesuite-backend/invoice"
	"stonesuite-backend/quote"
	"stonesuite-backend/workflow"
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
// mirroring the helper of the same name in estimate/quote/invoice's own tests.
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

// TestResolveRecordAccess_DocumentModules is the regression for the gap found
// while adding the attachment-required guard: ResolveRecordAccess previously
// only recognized sales_order among the four document-clone modules, so
// attachment endpoints 404'd for quote/estimate/invoice records even though
// workflow_record_attachments itself has no type restriction.
func TestResolveRecordAccess_DocumentModules(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	estIn := estimate.CreateEstimateInput{CustomerUUID: custUUID}
	estIn.Items = []estimate.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	est, err := estimate.Create(ctx, pool, estIn, 1)
	if err != nil {
		t.Fatalf("create estimate: %v", err)
	}
	if info, err := workflow.ResolveRecordAccess(ctx, pool, est.ID); err != nil {
		t.Fatalf("ResolveRecordAccess(estimate) = %v", err)
	} else if info.WorkflowKey != "estimate" {
		t.Errorf("WorkflowKey = %q, want %q", info.WorkflowKey, "estimate")
	}

	quoteIn := quote.CreateQuoteInput{CustomerUUID: custUUID}
	quoteIn.Items = []quote.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	qt, err := quote.Create(ctx, pool, quoteIn, 1)
	if err != nil {
		t.Fatalf("create quote: %v", err)
	}
	if info, err := workflow.ResolveRecordAccess(ctx, pool, qt.ID); err != nil {
		t.Fatalf("ResolveRecordAccess(quote) = %v", err)
	} else if info.WorkflowKey != "quote" {
		t.Errorf("WorkflowKey = %q, want %q", info.WorkflowKey, "quote")
	}

	inv, err := invoice.Create(ctx, pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	if info, err := workflow.ResolveRecordAccess(ctx, pool, inv.ID); err != nil {
		t.Fatalf("ResolveRecordAccess(invoice) = %v", err)
	} else if info.WorkflowKey != "invoice" {
		t.Errorf("WorkflowKey = %q, want %q", info.WorkflowKey, "invoice")
	}
}

// TestHasAttachments_TrueFalseAndIgnoresInfected covers HasAttachments'
// EXISTS predicate in isolation, independent of the four modules' own
// Transition-guard tests.
func TestHasAttachments_TrueFalseAndIgnoresInfected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	estIn := estimate.CreateEstimateInput{CustomerUUID: custUUID}
	estIn.Items = []estimate.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	est, err := estimate.Create(ctx, pool, estIn, 1)
	if err != nil {
		t.Fatalf("create estimate: %v", err)
	}

	has, err := workflow.HasAttachments(ctx, pool, est.ID)
	if err != nil {
		t.Fatalf("HasAttachments (none): %v", err)
	}
	if has {
		t.Error("HasAttachments = true, want false with zero rows")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_record_attachments
			(record_id, file_name, content_type, size_bytes, storage_key, status)
		VALUES ($1::uuid, 'infected.pdf', 'application/pdf', 100, $2, 'infected')`,
		est.ID, "test-key/"+est.ID+"/infected.pdf"); err != nil {
		t.Fatalf("seed infected attachment: %v", err)
	}
	has, err = workflow.HasAttachments(ctx, pool, est.ID)
	if err != nil {
		t.Fatalf("HasAttachments (infected only): %v", err)
	}
	if has {
		t.Error("HasAttachments = true, want false when only an infected row exists")
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO workflow_record_attachments
			(record_id, file_name, content_type, size_bytes, storage_key, status)
		VALUES ($1::uuid, 'clean.pdf', 'application/pdf', 100, $2, 'clean')`,
		est.ID, "test-key/"+est.ID+"/clean.pdf"); err != nil {
		t.Fatalf("seed clean attachment: %v", err)
	}
	has, err = workflow.HasAttachments(ctx, pool, est.ID)
	if err != nil {
		t.Fatalf("HasAttachments (clean present): %v", err)
	}
	if !has {
		t.Error("HasAttachments = false, want true once a clean row exists")
	}
}
