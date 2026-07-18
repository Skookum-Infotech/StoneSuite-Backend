// vendors/store_test.go
//go:build dbtest

package vendors

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/query"
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

func TestCreate_Person_RequiresGivenAndFamilyName(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateVendorInput{VendorType: "Person"}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create Person with no name = %v, want ClientError", err)
	}
}

func TestCreate_Organization_RequiresLegalName(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateVendorInput{VendorType: "Organization"}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create Organization with no legal name = %v, want ClientError", err)
	}
}

func TestCreate_UnknownVendorType_IsClientError(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateVendorInput{VendorType: "Robot"}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with unknown vendorType = %v, want ClientError", err)
	}
}

func TestCreate_Organization_SnapshotsAndAssignsNumber(t *testing.T) {
	pool := testPool(t)
	created, err := Create(context.Background(), pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: "Acme Supply Co"},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Number == "" || created.Number[:5] != "VNDR-" {
		t.Errorf("Number = %q, want VNDR- prefix", created.Number)
	}
	if created.StatusCode != "ACT_" {
		t.Errorf("StatusCode = %q, want ACT_", created.StatusCode)
	}
	if created.DisplayName != "Acme Supply Co" {
		t.Errorf("DisplayName = %q, want Acme Supply Co", created.DisplayName)
	}
}

func TestUpdate_ChangesEditableFieldsNotType(t *testing.T) {
	pool := testPool(t)
	created, err := Create(context.Background(), pool, CreateVendorInput{
		VendorType:   "Person",
		vendorFields: vendorFields{GivenName: "Jane", FamilyName: "Doe"},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := Update(context.Background(), pool, created.ID, UpdateVendorInput{
		VendorType:   "Organization", // ignored by Update; type is fixed at creation
		vendorFields: vendorFields{GivenName: "Jane", FamilyName: "Smith"},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.VendorType != "Person" {
		t.Errorf("VendorType after update = %q, want Person (fixed at creation)", updated.VendorType)
	}
	if updated.FamilyName != "Smith" {
		t.Errorf("FamilyName after update = %q, want Smith", updated.FamilyName)
	}
}

func TestSoftDelete_ThenGetReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	created, err := Create(context.Background(), pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: "Delete Me LLC"},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SoftDelete(context.Background(), pool, created.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := Get(context.Background(), pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestTransition_ActiveToOnHold(t *testing.T) {
	pool := testPool(t)
	created, err := Create(context.Background(), pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: "On Hold Inc"},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := Transition(context.Background(), pool, created.ID, "ONHD", 1)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if updated.StatusCode != "ONHD" {
		t.Errorf("StatusCode = %q, want ONHD", updated.StatusCode)
	}
}

func TestTransition_RejectsIllegalMove(t *testing.T) {
	pool := testPool(t)
	created, err := Create(context.Background(), pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: "Illegal Move Inc"},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// ACT_ has no self-transition configured.
	if _, err := Transition(context.Background(), pool, created.ID, "ACT_", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition ACT_->ACT_ = %v, want ErrInvalidTransition", err)
	}
}

func TestSearch_ReturnsCreatedVendor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created, err := Create(ctx, pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: "Findable Corp"},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	page, err := Search(ctx, pool, "all", "", query.Request{Search: created.Number})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range page.Records {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(%q) did not include the created vendor", created.Number)
	}
}
