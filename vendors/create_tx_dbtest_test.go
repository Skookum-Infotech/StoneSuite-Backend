//go:build dbtest

package vendors

import (
	"context"
	"testing"
)

func TestCreateTx_RollbackLeavesNoRow_CommitLeavesOne(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	rolled := uniqueName("CreateTx rollback")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	id, err := CreateTx(ctx, tx, CreateVendorInput{VendorType: "Organization", vendorFields: vendorFields{LegalName: rolled}}, 1)
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}
	if _, err := GetQ(ctx, tx, id); err != nil {
		t.Fatalf("GetQ inside tx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if _, err := Get(ctx, pool, id); err != ErrNotFound {
		t.Fatalf("Get after rollback = %v, want ErrNotFound", err)
	}

	committed := uniqueName("CreateTx commit")
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	id, err = CreateTx(ctx, tx, CreateVendorInput{VendorType: "Organization", vendorFields: vendorFields{LegalName: committed}}, 1)
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM vendor WHERE vendor_uuid = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows after commit = %d, want 1", n)
	}
}
