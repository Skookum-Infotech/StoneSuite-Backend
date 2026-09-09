//go:build dbtest

package quote

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

func newQuote(t *testing.T, pool *pgxpool.Pool, custUUID, itemUUID string) *Quote {
	t.Helper()
	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	q, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create quote: %v", err)
	}
	return q
}

func TestCreate_HonorsRecordNumberingConfig(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	enableNumbering(t, pool, "quote", "QT-TEST-", 4, 500)

	got := newQuote(t, pool, custUUID, itemUUID)
	if got.Number != "QT-TEST-0500" {
		t.Errorf("Number = %q, want QT-TEST-0500 (from Record Numbering config)", got.Number)
	}
}

func TestConvertFromEstimate_HonorsRecordNumberingConfig(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	est := seedEstimateWithLine(t, pool, custUUID, itemUUID)
	enableNumbering(t, pool, "quote", "QCONV-", 3, 7)

	got, _, err := ConvertFromEstimate(context.Background(), pool, est.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromEstimate: %v", err)
	}
	if got.Number != "QCONV-007" {
		t.Errorf("Number = %q, want QCONV-007 (from Record Numbering config)", got.Number)
	}
}

func TestCreate_WithoutConfigKeepsSerialDefault(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	got := newQuote(t, pool, custUUID, itemUUID)
	if len(got.Number) < 5 || got.Number[:5] != "QUOT-" {
		t.Errorf("Number = %q, want QUOT- serial default when no config is enabled", got.Number)
	}
}

func TestCreate_RecordNumberingCollisionIsClientError(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	enableNumbering(t, pool, "quote", "QDUP-", 4, 1)

	_ = newQuote(t, pool, custUUID, itemUUID) // claims QDUP-0001

	// Rewind the counter so the next create regenerates the same number.
	wf, err := workflow.GetWorkflowByKey(context.Background(), pool, "quote")
	if err != nil {
		t.Fatalf("resolve quote workflow: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE workflow_numbering_configs SET next_number = 1 WHERE workflow_id = $1`, wf.ID); err != nil {
		t.Fatalf("rewind counter: %v", err)
	}

	in := CreateQuoteInput{CustomerUUID: custUUID}
	in.Items = []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	_, err = Create(context.Background(), pool, in, 1)
	if !IsClientError(err) {
		t.Fatalf("create with colliding configured number = %v, want ClientError", err)
	}
}
