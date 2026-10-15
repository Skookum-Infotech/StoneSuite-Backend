//go:build dbtest

package inventory

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tryEnsureInTx runs EnsureCatalog in its own transaction and returns the error
// instead of failing the test, so goroutines can use it.
func tryEnsureInTx(pool *pgxpool.Pool, req CatalogRequest, commit bool) (*CatalogResult, error) {
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	res, err := EnsureCatalog(ctx, tx, req)
	if err != nil {
		return nil, err
	}
	if commit {
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
	}
	return res, nil
}

func ensureInTx(t *testing.T, pool *pgxpool.Pool, req CatalogRequest, commit bool) *CatalogResult {
	t.Helper()
	res, err := tryEnsureInTx(pool, req, commit)
	if err != nil {
		t.Fatalf("EnsureCatalog: %v", err)
	}
	return res
}

func TestEnsureCatalog_CreatesThenReusesByName(t *testing.T) {
	pool := testPool(t)
	kit, a, b := uniqueName("Kitchen"), uniqueName("Serene"), uniqueName("Flat")
	req := CatalogRequest{
		Kits:      []CatalogKit{{Name: kit, Items: []string{a, b}}},
		SKUPrefix: "PORTAL-", ActorEmployeeID: 1,
	}

	first := ensureInTx(t, pool, req, true)
	second := ensureInTx(t, pool, req, true)

	for key, ref := range first.Items {
		if !ref.Created {
			t.Errorf("item %q was not created on the first call", key)
		}
		if again := second.Items[key]; again.Created || again.ID != ref.ID {
			t.Errorf("item %q: second call = %+v, want the first call's row, not created", key, again)
		}
	}
	for key, ref := range first.Kits {
		if again := second.Kits[key]; again.Created || again.ID != ref.ID {
			t.Errorf("kit %q: second call = %+v, want the first call's row, not created", key, again)
		}
	}
}

func TestEnsureCatalog_RollbackLeavesNothing(t *testing.T) {
	pool := testPool(t)
	kit, item := uniqueName("Rolled Back Kit"), uniqueName("Rolled Back Item")

	ensureInTx(t, pool, CatalogRequest{Kits: []CatalogKit{{Name: kit, Items: []string{item}}}, SKUPrefix: "PORTAL-"}, false)

	var kits, items int
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM inventory_kit WHERE inventory_kit_name = $1`, kit).Scan(&kits); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM inventory_item WHERE inventory_item_name = $1`, item).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if kits != 0 || items != 0 {
		t.Errorf("after rollback: %d kits, %d items; want none", kits, items)
	}
}

func TestEnsureCatalog_ConcurrentCallersShareOneRowPerName(t *testing.T) {
	pool := testPool(t)
	kit, item := uniqueName("Shared Kit"), uniqueName("Shared Item")
	req := CatalogRequest{Kits: []CatalogKit{{Name: kit, Items: []string{item}}}, SKUPrefix: "PORTAL-"}

	var wg sync.WaitGroup
	ids := make([]string, 6)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := tryEnsureInTx(pool, req, true)
			if err != nil {
				t.Errorf("caller %d: %v", i, err)
				return
			}
			for _, r := range res.Items {
				ids[i] = r.ID
			}
		}(i)
	}
	wg.Wait()

	var items, kits int
	ctx := context.Background()
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM inventory_item WHERE inventory_item_name = $1`, item).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM inventory_kit WHERE inventory_kit_name = $1`, kit).Scan(&kits); err != nil {
		t.Fatal(err)
	}
	if items != 1 || kits != 1 {
		t.Errorf("%d items, %d kits created by 6 callers; want exactly one of each", items, kits)
	}
	for _, id := range ids {
		if id != ids[0] {
			t.Errorf("callers resolved to different items: %v", ids)
			break
		}
	}
}
