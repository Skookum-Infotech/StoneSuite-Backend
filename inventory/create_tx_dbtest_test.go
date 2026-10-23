//go:build dbtest

package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func txItemInput(prefix string) CreateItemInput {
	return CreateItemInput{
		SKU:               fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()),
		Name:              uniqueName(prefix),
		UnitID:            1,
		UnitPrice:         priceOf(10),
		PurchaseUnitPrice: priceOf(6),
	}
}

func TestCreateTx_RollbackLeavesNoRow_CommitLeavesOne(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	id, err := CreateTx(ctx, tx, txItemInput("TXRB"), 1)
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

	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	id, err = CreateTx(ctx, tx, txItemInput("TXOK"), 1)
	if err != nil {
		t.Fatalf("CreateTx: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM inventory_item WHERE inventory_item_uuid = $1`, id).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows after commit = %d, want 1", n)
	}
}
