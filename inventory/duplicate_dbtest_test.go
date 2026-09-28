//go:build dbtest

package inventory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/duplicate"
)

// createNamed creates a live item called name under a fresh SKU, so a test can
// vary the name alone. unit_id 1 ("EA") is seeded by the tenant template.
func createNamed(t *testing.T, pool *pgxpool.Pool, name string) *Item {
	t.Helper()
	item, err := Create(context.Background(), pool, CreateItemInput{
		SKU:       fmt.Sprintf("SKU-%d", time.Now().UnixNano()),
		Name:      name,
		UnitID:    1,
		UnitPrice: 10,
	}, 1)
	if err != nil {
		t.Fatalf("Create %q: %v", name, err)
	}
	return item
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s %d", prefix, time.Now().UnixNano())
}

func requireDuplicate(t *testing.T, err error, wantID string) *duplicate.Error {
	t.Helper()
	d, ok := duplicate.As(err)
	if !ok {
		t.Fatalf("err = %v, want a *duplicate.Error", err)
	}
	if d.Entity != duplicate.EntityItem {
		t.Errorf("Entity = %q, want %q", d.Entity, duplicate.EntityItem)
	}
	if d.ExistingID != wantID {
		t.Errorf("ExistingID = %q, want %q", d.ExistingID, wantID)
	}
	return d
}

func TestCreate_SameNameIsRejected_EvenUnderAnotherSKU(t *testing.T) {
	pool := testPool(t)
	name := uniqueName("Carrara White 3cm")
	first := createNamed(t, pool, name)

	for _, variant := range []string{name, strings.ToUpper(name), "  " + strings.ToLower(name) + "  ", strings.Replace(name, " ", "   ", 1)} {
		_, err := Create(context.Background(), pool, CreateItemInput{
			SKU:    fmt.Sprintf("OTHER-%d", time.Now().UnixNano()),
			Name:   variant,
			UnitID: 1,
		}, 1)
		requireDuplicate(t, err, first.ID)
	}
}

// An inactive item is hidden from every picker that lists only usable ones, so
// it is exactly the record a duplicate would slip past.
func TestCreate_InactiveItemStillBlocksTheName(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	name := uniqueName("Retired Slab")
	first := createNamed(t, pool, name)
	if _, err := pool.Exec(ctx,
		`UPDATE inventory_item SET inventory_item_is_active = FALSE WHERE inventory_item_uuid = $1`, first.ID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	_, err := Create(ctx, pool, CreateItemInput{SKU: fmt.Sprintf("SKU-%d", time.Now().UnixNano()), Name: name, UnitID: 1}, 1)
	d := requireDuplicate(t, err, first.ID)
	if d.ExistingStatus != "Inactive" {
		t.Errorf("ExistingStatus = %q, want Inactive", d.ExistingStatus)
	}
}

func TestCreate_SoftDeletedItemFreesTheName(t *testing.T) {
	pool := testPool(t)
	name := uniqueName("Discontinued Slab")
	first := createNamed(t, pool, name)
	if err := SoftDelete(context.Background(), pool, first.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	createNamed(t, pool, name)
}

func TestUpdate_RenameOntoAnExistingItemIsRejected(t *testing.T) {
	pool := testPool(t)
	taken := createNamed(t, pool, uniqueName("Taken Slab"))
	other := createNamed(t, pool, uniqueName("Other Slab"))

	err := Update(context.Background(), pool, other.ID, CreateItemInput{SKU: other.SKU, Name: taken.Name, UnitID: 1}, 1)
	requireDuplicate(t, err, taken.ID)
}

func TestUpdate_KeepingTheNameOrRenamingToAFreeOneIsAllowed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := createNamed(t, pool, uniqueName("Steady Slab"))

	if err := Update(ctx, pool, item.ID, CreateItemInput{SKU: item.SKU, Name: item.Name, UnitID: 1, UnitPrice: 99}, 1); err != nil {
		t.Fatalf("Update keeping the same name: %v", err)
	}
	if err := Update(ctx, pool, item.ID, CreateItemInput{SKU: item.SKU, Name: uniqueName("Renamed Slab"), UnitID: 1}, 1); err != nil {
		t.Fatalf("Update to a free name: %v", err)
	}
}

// A tenant may already hold same-named items from before this rule. Editing one
// for any other reason must keep working — only a rename is checked.
func TestUpdate_PreExistingDuplicateCanStillBeEdited(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := createNamed(t, pool, uniqueName("Twin Slab"))
	b := createNamed(t, pool, uniqueName("Twin Slab Two"))
	if _, err := pool.Exec(ctx, `UPDATE inventory_item SET inventory_item_name = $2 WHERE inventory_item_uuid = $1`, b.ID, a.Name); err != nil {
		t.Fatalf("force a duplicate name: %v", err)
	}

	if err := Update(ctx, pool, b.ID, CreateItemInput{SKU: b.SKU, Name: a.Name, UnitID: 1, UnitPrice: 5}, 1); err != nil {
		t.Fatalf("Update of a pre-existing duplicate (name unchanged): %v", err)
	}
}
