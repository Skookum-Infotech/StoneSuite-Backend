//go:build dbtest

package companyprofile

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
	// company_profile is a singleton table shared by every test in this
	// package (there is no per-tenant id to scope by) -- reset it up front
	// so tests don't inherit state left behind by whichever test ran before
	// them (Go does not guarantee execution order across test functions).
	if _, err := pool.Exec(context.Background(), `DELETE FROM company_profile`); err != nil {
		t.Fatalf("reset company_profile: %v", err)
	}
	return pool
}

func TestGet_NoRowYet_ReturnsZeroValue(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if *got != (Profile{}) {
		t.Errorf("Get() = %+v, want zero-value Profile", *got)
	}
}

func TestUpsert_ThenGet_RoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	want := Profile{
		CompanyName:     "Acme Stone Co.",
		LegalName:       "Acme Stone Company LLC",
		Industry:        "Fabrication",
		Website:         "https://acmestone.example",
		Country:         "United States",
		Currency:        "USD",
		Timezone:        "America/Chicago",
		TaxID:           "12-3456789",
		BillingAddress:  "123 Main St\nSpringfield, IL 62704",
		ShippingAddress: "456 Warehouse Ave\nSpringfield, IL 62704",
		ReturnAddress:   "789 Returns Dock\nSpringfield, IL 62704",
	}
	if err := Upsert(ctx, pool, want); err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}

	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if *got != want {
		t.Errorf("Get() = %+v, want %+v", *got, want)
	}
}

func TestUpsert_Twice_UpdatesSingletonRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := Upsert(ctx, pool, Profile{CompanyName: "First Name"}); err != nil {
		t.Fatalf("first Upsert() error = %v, want nil", err)
	}
	if err := Upsert(ctx, pool, Profile{CompanyName: "Second Name"}); err != nil {
		t.Fatalf("second Upsert() error = %v, want nil", err)
	}

	var rowCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM company_profile`).Scan(&rowCount); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rowCount != 1 {
		t.Errorf("row count = %d, want 1 (singleton table)", rowCount)
	}

	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.CompanyName != "Second Name" {
		t.Errorf("CompanyName = %q, want %q", got.CompanyName, "Second Name")
	}
}

func TestUpsert_InvalidProfile_ReturnsErrorAndDoesNotSave(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	err := Upsert(ctx, pool, Profile{CompanyName: ""})
	if err == nil {
		t.Fatal("Upsert() error = nil, want error for empty companyName")
	}

	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.CompanyName != "" {
		t.Errorf("CompanyName = %q, want empty (invalid upsert must not persist)", got.CompanyName)
	}
}
