//go:build dbtest

package companyprofile

import (
	"context"
	"os"
	"reflect"
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
	if !reflect.DeepEqual(*got, Profile{}) {
		t.Errorf("Get() = %+v, want zero-value Profile", *got)
	}
}

func TestUpsert_ThenGet_RoundTrips(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	want := Profile{
		CompanyName: "Acme Stone Co.",
		LegalName:   "Acme Stone Company LLC",
		Industry:    "Fabrication",
		Website:     "https://acmestone.example",
		Country:     "United States",
		Currency:    "USD",
		Timezone:    "America/Chicago",
		TaxID:       "12-3456789",
		BillingAddress: Address{
			Line1: "123 Main St", Suite: "400", City: "Springfield", Country: "United States", State: "IL", Zip: "62704",
		},
		ShippingAddress: Address{
			Line1: "456 Warehouse Ave", City: "Springfield", Country: "United States", State: "IL", Zip: "62704",
		},
		ReturnAddress: Address{
			Line1: "789 Returns Dock", City: "Springfield", Country: "United States", State: "IL", Zip: "62704",
		},
		PaymentDetails: &PaymentDetails{BankName: "Chase Bank", AccountNumber: "000123456789", RoutingNumber: "021000021"},
	}
	if err := Upsert(ctx, pool, want); err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}

	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("Get() = %+v, want %+v", *got, want)
	}
}

func TestUpsert_PaymentDetails(t *testing.T) {
	saved := &PaymentDetails{BankName: "Chase Bank", AccountNumber: "000123456789", RoutingNumber: "021000021"}
	tests := []struct {
		name   string
		second *PaymentDetails // sent on the second save; the first always stores `saved`
		want   *PaymentDetails // nil once cleared: Get reports "none stored" as nil
	}{
		{"omitted keeps what is stored", nil, saved},
		{"a new value replaces it", &PaymentDetails{BankName: "Wells Fargo", AccountNumber: "999", RoutingNumber: "121000248"},
			&PaymentDetails{BankName: "Wells Fargo", AccountNumber: "999", RoutingNumber: "121000248"}},
		{"an empty object clears it", &PaymentDetails{}, nil},
		{"whitespace is trimmed", &PaymentDetails{BankName: "  Chase  ", AccountNumber: " 1 ", RoutingNumber: "2 "},
			&PaymentDetails{BankName: "Chase", AccountNumber: "1", RoutingNumber: "2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := testPool(t)
			ctx := context.Background()

			if err := Upsert(ctx, pool, Profile{CompanyName: "Acme", PaymentDetails: saved}); err != nil {
				t.Fatalf("first Upsert() error = %v, want nil", err)
			}
			if err := Upsert(ctx, pool, Profile{CompanyName: "Acme Renamed", PaymentDetails: tt.second}); err != nil {
				t.Fatalf("second Upsert() error = %v, want nil", err)
			}

			got, err := Get(ctx, pool)
			if err != nil {
				t.Fatalf("Get() error = %v, want nil", err)
			}
			if got.CompanyName != "Acme Renamed" {
				t.Errorf("CompanyName = %q, want the second save applied", got.CompanyName)
			}
			if !reflect.DeepEqual(got.PaymentDetails, tt.want) {
				t.Errorf("PaymentDetails = %+v, want %+v", got.PaymentDetails, tt.want)
			}
		})
	}
}

func TestUpsert_FirstSaveWithoutPaymentDetails_LeavesThemUnset(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := Upsert(ctx, pool, Profile{CompanyName: "Acme"}); err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}
	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.PaymentDetails != nil {
		t.Errorf("PaymentDetails = %+v, want nil (none stored)", got.PaymentDetails)
	}
}

func TestSetLogoKey_KeepsPaymentDetails(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	want := PaymentDetails{BankName: "Chase Bank", AccountNumber: "000123456789", RoutingNumber: "021000021"}
	if err := Upsert(ctx, pool, Profile{CompanyName: "Acme", PaymentDetails: &want}); err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}
	if err := SetLogoKey(ctx, pool, "company/logo.png"); err != nil {
		t.Fatalf("SetLogoKey() error = %v, want nil", err)
	}
	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.PaymentDetails == nil || *got.PaymentDetails != want {
		t.Errorf("PaymentDetails = %+v, want %+v (SetLogoKey must not touch them)", got.PaymentDetails, want)
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

func TestSetLogoKey_RoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// SetLogoKey must work even before any profile row exists.
	if err := SetLogoKey(ctx, pool, "company/logo.png"); err != nil {
		t.Fatalf("SetLogoKey() error = %v, want nil", err)
	}
	got, err := Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.LogoKey != "company/logo.png" {
		t.Errorf("LogoKey = %q, want %q", got.LogoKey, "company/logo.png")
	}

	// Clearing (empty string) must work and must not touch other fields.
	if err := Upsert(ctx, pool, Profile{CompanyName: "Acme"}); err != nil {
		t.Fatalf("Upsert() error = %v, want nil", err)
	}
	if err := SetLogoKey(ctx, pool, ""); err != nil {
		t.Fatalf("SetLogoKey(\"\") error = %v, want nil", err)
	}
	got, err = Get(ctx, pool)
	if err != nil {
		t.Fatalf("Get() error = %v, want nil", err)
	}
	if got.LogoKey != "" {
		t.Errorf("LogoKey = %q, want empty after clear", got.LogoKey)
	}
	if got.CompanyName != "Acme" {
		t.Errorf("CompanyName = %q, want %q (SetLogoKey must not clobber other fields)", got.CompanyName, "Acme")
	}
}
