//go:build dbtest

package companylocation

import (
	"context"
	"os"
	"testing"

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
	// Every test in this package shares the same table with no per-tenant id
	// to scope by -- reset it up front so tests don't inherit state left
	// behind by whichever test ran before them (Go does not guarantee
	// execution order across test functions).
	if _, err := pool.Exec(context.Background(), `DELETE FROM company_location`); err != nil {
		t.Fatalf("reset company_location: %v", err)
	}
	return pool
}

func TestList_NoRows_ReturnsEmptySlice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	got, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("List() = %+v, want empty slice", got)
	}
}

func TestCreate_First_BecomesDefault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	got, err := Create(ctx, pool, Input{Name: "Main Office"}, 1)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}
	if !got.IsDefault {
		t.Errorf("Create() first location IsDefault = false, want true")
	}
}

func TestCreate_Second_DoesNotBecomeDefault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Create(ctx, pool, Input{Name: "Main Office"}, 1); err != nil {
		t.Fatalf("first Create() error = %v, want nil", err)
	}
	second, err := Create(ctx, pool, Input{Name: "Warehouse 2"}, 1)
	if err != nil {
		t.Fatalf("second Create() error = %v, want nil", err)
	}
	if second.IsDefault {
		t.Errorf("Create() second location IsDefault = true, want false")
	}
}

func TestCreate_InvalidInput_ReturnsErrorAndDoesNotSave(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Create(ctx, pool, Input{Name: ""}, 1); err == nil {
		t.Fatal("Create() error = nil, want error for empty name")
	}

	got, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("List() = %+v, want empty (invalid create must not persist)", got)
	}
}

func TestGet_UnknownID_ReturnsErrNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	_, err := Get(ctx, pool, "00000000-0000-0000-0000-000000000000")
	if err != ErrNotFound {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestGet_MalformedID_ReturnsErrNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	_, err := Get(ctx, pool, "not-a-uuid")
	if err != ErrNotFound {
		t.Errorf("Get() error = %v, want ErrNotFound", err)
	}
}

func TestUpdate_RoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, Input{Name: "Main Office"}, 1)
	if err != nil {
		t.Fatalf("Create() error = %v, want nil", err)
	}

	want := Input{
		Name:  "Renamed Office",
		Phone: "555-0100",
		Address: Address{
			Line1: "123 Main St", City: "Springfield", Country: "United States", State: "IL", Zip: "62704",
		},
	}
	if err := Update(ctx, pool, created.ID, want); err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}

	got, err := Get(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.Name != want.Name || got.Phone != want.Phone || got.Address != want.Address {
		t.Errorf("Get() after Update = %+v, want Name/Phone/Address %+v", got, want)
	}
}

func TestUpdate_DoesNotChangeIsDefault(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, Input{Name: "Main Office"}, 1)
	if err != nil || !created.IsDefault {
		t.Fatalf("Create() = %+v, err = %v, want first location default", created, err)
	}

	if err := Update(ctx, pool, created.ID, Input{Name: "Renamed"}); err != nil {
		t.Fatalf("Update() error = %v, want nil", err)
	}

	got, err := Get(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if !got.IsDefault {
		t.Errorf("Get() after Update IsDefault = false, want true (Update must not touch is_default)")
	}
}

func TestUpdate_UnknownID_ReturnsErrNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	err := Update(ctx, pool, "00000000-0000-0000-0000-000000000000", Input{Name: "X"})
	if err != ErrNotFound {
		t.Errorf("Update() error = %v, want ErrNotFound", err)
	}
}

func TestSetDefault_MovesFlagAndClearsPrevious(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	first, err := Create(ctx, pool, Input{Name: "Main Office"}, 1)
	if err != nil {
		t.Fatalf("first Create() error = %v, want nil", err)
	}
	second, err := Create(ctx, pool, Input{Name: "Warehouse 2"}, 1)
	if err != nil {
		t.Fatalf("second Create() error = %v, want nil", err)
	}

	if err := SetDefault(ctx, pool, second.ID); err != nil {
		t.Fatalf("SetDefault() error = %v, want nil", err)
	}

	gotFirst, err := Get(ctx, pool, first.ID)
	if err != nil {
		t.Fatalf("Get(first) error = %v, want nil", err)
	}
	if gotFirst.IsDefault {
		t.Errorf("Get(first).IsDefault = true, want false after moving default away")
	}
	gotSecond, err := Get(ctx, pool, second.ID)
	if err != nil {
		t.Fatalf("Get(second) error = %v, want nil", err)
	}
	if !gotSecond.IsDefault {
		t.Errorf("Get(second).IsDefault = false, want true after SetDefault")
	}
}

func TestSetDefault_UnknownID_ReturnsErrNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	err := SetDefault(ctx, pool, "00000000-0000-0000-0000-000000000000")
	if err != ErrNotFound {
		t.Errorf("SetDefault() error = %v, want ErrNotFound", err)
	}
}

func TestDelete_NonDefault_Succeeds(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Create(ctx, pool, Input{Name: "Main Office"}, 1); err != nil {
		t.Fatalf("first Create() error = %v, want nil", err)
	}
	second, err := Create(ctx, pool, Input{Name: "Warehouse 2"}, 1)
	if err != nil {
		t.Fatalf("second Create() error = %v, want nil", err)
	}

	if err := Delete(ctx, pool, second.ID, 1); err != nil {
		t.Fatalf("Delete() error = %v, want nil", err)
	}
	if _, err := Get(ctx, pool, second.ID); err != ErrNotFound {
		t.Errorf("Get() after Delete error = %v, want ErrNotFound", err)
	}
}

func TestDelete_DefaultWithOthersRemaining_Blocked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	first, err := Create(ctx, pool, Input{Name: "Main Office"}, 1)
	if err != nil || !first.IsDefault {
		t.Fatalf("Create() = %+v, err = %v, want first location default", first, err)
	}
	if _, err := Create(ctx, pool, Input{Name: "Warehouse 2"}, 1); err != nil {
		t.Fatalf("second Create() error = %v, want nil", err)
	}

	err = Delete(ctx, pool, first.ID, 1)
	if !IsClientError(err) {
		t.Fatalf("Delete() error = %v, want a ClientError blocking the delete", err)
	}

	if _, err := Get(ctx, pool, first.ID); err != nil {
		t.Errorf("Get() after blocked Delete error = %v, want nil (location must still exist)", err)
	}
}

func TestDelete_DefaultAsOnlyLocation_Allowed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	only, err := Create(ctx, pool, Input{Name: "Main Office"}, 1)
	if err != nil || !only.IsDefault {
		t.Fatalf("Create() = %+v, err = %v, want the only location default", only, err)
	}

	if err := Delete(ctx, pool, only.ID, 1); err != nil {
		t.Fatalf("Delete() error = %v, want nil (deleting the sole location must be allowed)", err)
	}

	got, err := List(ctx, pool)
	if err != nil {
		t.Fatalf("List() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("List() after Delete = %+v, want empty", got)
	}
}

func TestDelete_UnknownID_ReturnsErrNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	err := Delete(ctx, pool, "00000000-0000-0000-0000-000000000000", 1)
	if err != ErrNotFound {
		t.Errorf("Delete() error = %v, want ErrNotFound", err)
	}
}
