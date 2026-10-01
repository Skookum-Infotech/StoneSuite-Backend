//go:build dbtest

package onboardingseed

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/companylocation"
	"stonesuite-backend/companyprofile"
)

const fullMetadata = `{
	"company_name": "Acme Stone Co.",
	"country": "United States of America",
	"currency": "USD",
	"billing_address_line1": "123 Main St",
	"billing_address_city": "Springfield",
	"billing_address_state": "Illinois",
	"location_name": "Main Showroom",
	"location_phone": "+1 555 123 4567",
	"location_address_line1": "9 Quarry Rd",
	"location_address_city": "Springfield",
	"location_address_country": "United States of America",
	"location_address_state": "Illinois",
	"location_address_zip": "62704"
}`

// testPool resets both singleton-ish tables up front: every test in this
// package shares them with no per-tenant id to scope by, so tests must not
// inherit whatever the previous one left behind.
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
	for _, table := range []string{"company_profile", "company_location"} {
		if _, err := pool.Exec(context.Background(), `DELETE FROM `+table); err != nil {
			t.Fatalf("reset %s: %v", table, err)
		}
	}
	return pool
}

func TestSeed_WritesProfileAndDefaultLocation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	res, err := Seed(ctx, pool, fullMetadata, false)
	if err != nil {
		t.Fatalf("Seed() error = %v, want nil", err)
	}
	if !res.Profile || !res.Location {
		t.Fatalf("Seed() = %+v, want both Profile and Location written", res)
	}

	profile, err := companyprofile.Get(ctx, pool)
	if err != nil {
		t.Fatalf("companyprofile.Get() error = %v", err)
	}
	if profile.CompanyName != "Acme Stone Co." || profile.BillingAddress.City != "Springfield" {
		t.Errorf("profile = %+v, want company name and billing city from the metadata", profile)
	}

	locations, err := companylocation.List(ctx, pool)
	if err != nil {
		t.Fatalf("companylocation.List() error = %v", err)
	}
	if len(locations) != 1 {
		t.Fatalf("locations = %+v, want exactly 1", locations)
	}
	if got := locations[0]; got.Name != "Main Showroom" || got.Address.State != "Illinois" || !got.IsDefault {
		t.Errorf("location = %+v, want the metadata's location as the default", got)
	}
}

func TestSeed_DryRun_WritesNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	res, err := Seed(ctx, pool, fullMetadata, true)
	if err != nil {
		t.Fatalf("Seed(dryRun) error = %v, want nil", err)
	}
	if !res.Profile || !res.Location {
		t.Errorf("Seed(dryRun) = %+v, want it to report both would be written", res)
	}

	if locations, _ := companylocation.List(ctx, pool); len(locations) != 0 {
		t.Errorf("locations after dry run = %+v, want none", locations)
	}
	if profile, _ := companyprofile.Get(ctx, pool); *profile != (companyprofile.Profile{}) {
		t.Errorf("profile after dry run = %+v, want empty", profile)
	}
}

func TestSeed_Idempotent(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Seed(ctx, pool, fullMetadata, false); err != nil {
		t.Fatalf("first Seed() error = %v", err)
	}
	res, err := Seed(ctx, pool, fullMetadata, false)
	if err != nil {
		t.Fatalf("second Seed() error = %v", err)
	}
	if res.Any() {
		t.Errorf("second Seed() = %+v, want nothing written", res)
	}
	if locations, _ := companylocation.List(ctx, pool); len(locations) != 1 {
		t.Errorf("locations after re-seed = %d, want still 1", len(locations))
	}
}

func TestSeed_DoesNotOverwriteExistingProfile(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if err := companyprofile.Upsert(ctx, pool, companyprofile.Profile{CompanyName: "Edited By Hand"}); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	res, err := Seed(ctx, pool, fullMetadata, false)
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	if res.Profile {
		t.Errorf("Seed() overwrote a profile that was already set")
	}
	if profile, _ := companyprofile.Get(ctx, pool); profile.CompanyName != "Edited By Hand" {
		t.Errorf("profile.CompanyName = %q, want it untouched", profile.CompanyName)
	}
	if !res.Location {
		t.Errorf("Seed() = %+v, want the location still seeded independently", res)
	}
}

func TestSeed_DoesNotResurrectADeletedLocation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := Seed(ctx, pool, fullMetadata, false); err != nil {
		t.Fatalf("first Seed() error = %v", err)
	}
	locations, _ := companylocation.List(ctx, pool)
	if err := companylocation.Delete(ctx, pool, locations[0].ID, 0); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	res, err := Seed(ctx, pool, fullMetadata, false)
	if err != nil {
		t.Fatalf("second Seed() error = %v", err)
	}
	if res.Location {
		t.Errorf("Seed() re-created a location the tenant deleted")
	}
}

func TestSeed_NoUsableMetadata_IsANoOp(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	for _, metadata := range []string{"", "{}", "not json", `{"industry": "Fabrication"}`} {
		res, err := Seed(ctx, pool, metadata, false)
		if err != nil {
			t.Errorf("Seed(%q) error = %v, want nil", metadata, err)
		}
		if res.Any() {
			t.Errorf("Seed(%q) = %+v, want nothing written", metadata, res)
		}
	}
}

func TestSeed_ProfileOnlyMetadata_LeavesLocationsEmpty(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	res, err := Seed(ctx, pool, `{"company_name": "Acme Stone Co."}`, false)
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	if !res.Profile || res.Location {
		t.Errorf("Seed() = %+v, want profile only", res)
	}
	if locations, _ := companylocation.List(ctx, pool); len(locations) != 0 {
		t.Errorf("locations = %+v, want none (no billing-address fallback row)", locations)
	}
}

func TestSeed_InvalidProfile_StillSeedsLocation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	tooLong := make([]byte, companyprofile.MaxFieldLength+1)
	for i := range tooLong {
		tooLong[i] = 'x'
	}
	metadata := `{"company_name": "` + string(tooLong) + `", "location_name": "HQ"}`

	res, err := Seed(ctx, pool, metadata, false)
	if err == nil {
		t.Fatal("Seed() error = nil, want the profile validation failure reported")
	}
	if res.Profile {
		t.Errorf("Seed() reported a profile written despite failing validation")
	}
	if !res.Location {
		t.Errorf("Seed() = %+v, want the location seeded despite the profile failing", res)
	}
}
