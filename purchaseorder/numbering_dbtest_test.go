//go:build dbtest

package purchaseorder

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// enableNumbering turns on Record Numbering for workflowKey with the given
// format and registers cleanup that removes the config row again — other
// dbtests in this package assert on the serial-format default, so a leaked
// config row would make them order-dependent.
func enableNumbering(t *testing.T, pool *pgxpool.Pool, workflowKey, prefix string, minDigits int, next int64) {
	t.Helper()
	ctx := context.Background()
	wf, err := workflow.GetWorkflowByKey(ctx, pool, workflowKey)
	if err != nil {
		t.Fatalf("resolve %q workflow: %v", workflowKey, err)
	}
	if err := workflow.UpsertNumberingConfig(ctx, pool, workflow.NumberingConfig{
		WorkflowID: wf.ID, Enabled: true, Prefix: prefix, MinDigits: minDigits, NextNumber: next,
	}); err != nil {
		t.Fatalf("enable numbering for %q: %v", workflowKey, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM workflow_numbering_configs WHERE workflow_id = $1`, wf.ID)
	})
}

func TestCreate_HonorsRecordNumberingConfig(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	enableNumbering(t, pool, "purchase_order", "PO-TEST-", 5, 88)

	in := CreatePurchaseOrderInput{VendorUUID: vendorUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Number != "PO-TEST-00088" {
		t.Errorf("Number = %q, want PO-TEST-00088 (from Record Numbering config)", got.Number)
	}
}

func TestCreate_WithoutConfigKeepsSerialDefault(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)

	in := CreatePurchaseOrderInput{VendorUUID: vendorUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(got.Number) < 5 || got.Number[:5] != "PORD-" {
		t.Errorf("Number = %q, want PORD- serial default when no config is enabled", got.Number)
	}
}
