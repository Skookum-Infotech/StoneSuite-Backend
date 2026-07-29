// inventory/store_test.go
//go:build dbtest

package inventory

import (
	"context"
	"errors"
	"fmt"
	"os"
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

// newItem creates a live inventory item with a unique SKU. unit_id 1 ("EA") is
// seeded by the tenant template.
func newItem(t *testing.T, pool *pgxpool.Pool, actorEmployeeID int) *Item {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	item, err := Create(context.Background(), pool, CreateItemInput{
		SKU:       "SKU-" + suffix,
		Name:      "Test Item " + suffix,
		UnitID:    1,
		UnitPrice: 25.00,
	}, actorEmployeeID)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return item
}

func TestSoftDelete_ThenGetReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created := newItem(t, pool, 1)

	if err := SoftDelete(ctx, pool, created.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := Get(ctx, pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

// TestSoftDelete_UnresolvedActor is the regression for the module-wide
// soft-delete defect: resolveEmployeeID (controllers/crm_admin.go) is
// best-effort and returns 0 whenever the caller has no linked employee row —
// the common case, since nothing populates employee.employee_user_id. Binding
// that 0 through nullableInt wrote SQL NULL into inventory_item_deleted_by,
// which chk_inventory_item_soft_delete rejects, so delete failed with a
// wrapped 500.
func TestSoftDelete_UnresolvedActor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created := newItem(t, pool, 1)

	if err := SoftDelete(ctx, pool, created.ID, 0); err != nil {
		t.Fatalf("SoftDelete with unresolved actor: %v", err)
	}
	var deletedBy int
	if err := pool.QueryRow(ctx,
		`SELECT inventory_item_deleted_by FROM inventory_item WHERE inventory_item_uuid = $1`, created.ID,
	).Scan(&deletedBy); err != nil {
		t.Fatalf("read inventory_item_deleted_by: %v", err)
	}
	if deletedBy != systemEmployeeID {
		t.Errorf("inventory_item_deleted_by = %d, want %d (system employee)", deletedBy, systemEmployeeID)
	}
}
