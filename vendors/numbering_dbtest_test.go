//go:build dbtest

package vendors

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
	enableNumbering(t, pool, "vendor", "SUPP-", 4, 42)

	created, err := Create(context.Background(), pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: "Acme Supply Co"},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Number != "SUPP-0042" {
		t.Errorf("Number = %q, want SUPP-0042 (from Record Numbering config)", created.Number)
	}
}

func TestCreate_WithoutConfigKeepsSerialDefault(t *testing.T) {
	pool := testPool(t)

	created, err := Create(context.Background(), pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: "Default Format Co"},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.Number) < 5 || created.Number[:5] != "VNDR-" {
		t.Errorf("Number = %q, want VNDR- serial default when no config is enabled", created.Number)
	}
}
