//go:build dbtest

package vendors

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/duplicate"
)

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s %d", prefix, time.Now().UnixNano())
}

func createOrg(t *testing.T, pool *pgxpool.Pool, legalName string) *Vendor {
	t.Helper()
	v, err := Create(context.Background(), pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: legalName},
	}, 1)
	if err != nil {
		t.Fatalf("Create %q: %v", legalName, err)
	}
	return v
}

func requireDuplicate(t *testing.T, err error, wantID string) *duplicate.Error {
	t.Helper()
	d, ok := duplicate.As(err)
	if !ok {
		t.Fatalf("err = %v, want a *duplicate.Error", err)
	}
	if d.Entity != duplicate.EntityVendor {
		t.Errorf("Entity = %q, want %q", d.Entity, duplicate.EntityVendor)
	}
	if d.ExistingID != wantID {
		t.Errorf("ExistingID = %q, want %q", d.ExistingID, wantID)
	}
	return d
}

func TestCreate_SameNameIsRejected_WhateverTheCaseOrSpacing(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	name := uniqueName("Nero Marble Co")
	first := createOrg(t, pool, name)

	for _, variant := range []string{
		name,
		strings.ToUpper(name),
		"  " + strings.ToLower(name) + "  ",
		strings.Replace(name, " ", "   ", 1),
	} {
		_, err := Create(ctx, pool, CreateVendorInput{
			VendorType:   "Organization",
			vendorFields: vendorFields{LegalName: variant},
		}, 1)
		requireDuplicate(t, err, first.ID)
	}
}

// An Inactive vendor is invisible to any picker that lists only usable ones,
// so it is exactly the record a duplicate would slip past.
func TestCreate_InactiveVendorStillBlocksTheName(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	name := uniqueName("Dormant Quarry")
	first := createOrg(t, pool, name)
	if _, err := Transition(ctx, pool, first.ID, "INA_", 1); err != nil {
		t.Fatalf("Transition to inactive: %v", err)
	}

	_, err := Create(ctx, pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: name},
	}, 1)
	d := requireDuplicate(t, err, first.ID)
	if d.ExistingStatus != "Inactive" {
		t.Errorf("ExistingStatus = %q, want Inactive", d.ExistingStatus)
	}
}

func TestCreate_SoftDeletedVendorFreesTheName(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	name := uniqueName("Closed Yard")
	first := createOrg(t, pool, name)
	if err := SoftDelete(ctx, pool, first.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}

	if _, err := Create(ctx, pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: name},
	}, 1); err != nil {
		t.Fatalf("Create after the original was deleted: %v", err)
	}
}

// A person is named by given + family name. The honorific is not part of the
// identity, and a business with the same name is the same duplicate.
func TestCreate_PersonNameIgnoresHonorificAndMatchesAnOrganization(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	family := uniqueName("Lovelace")
	person, err := Create(ctx, pool, CreateVendorInput{
		VendorType:   "Person",
		vendorFields: vendorFields{HonorificPrefix: "Dr.", GivenName: "Ada", FamilyName: family},
	}, 1)
	if err != nil {
		t.Fatalf("Create person: %v", err)
	}

	_, err = Create(ctx, pool, CreateVendorInput{
		VendorType:   "Person",
		vendorFields: vendorFields{GivenName: "ADA", FamilyName: strings.ToUpper(family)},
	}, 1)
	requireDuplicate(t, err, person.ID)

	_, err = Create(ctx, pool, CreateVendorInput{
		VendorType:   "Organization",
		vendorFields: vendorFields{LegalName: "Ada " + family},
	}, 1)
	requireDuplicate(t, err, person.ID)
}

func TestUpdate_RenameOntoAnExistingNameIsRejected(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	taken := createOrg(t, pool, uniqueName("Taken Stone"))
	other := createOrg(t, pool, uniqueName("Other Stone"))

	_, err := Update(ctx, pool, other.ID, UpdateVendorInput{
		vendorFields: vendorFields{LegalName: taken.LegalName},
	}, 1)
	requireDuplicate(t, err, taken.ID)
}

func TestUpdate_KeepingTheNameOrRenamingToAFreeOneIsAllowed(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	v := createOrg(t, pool, uniqueName("Steady Stone"))

	if _, err := Update(ctx, pool, v.ID, UpdateVendorInput{
		vendorFields: vendorFields{LegalName: v.LegalName, Email: "orders@example.com"},
	}, 1); err != nil {
		t.Fatalf("Update keeping the same name: %v", err)
	}
	if _, err := Update(ctx, pool, v.ID, UpdateVendorInput{
		vendorFields: vendorFields{LegalName: uniqueName("Renamed Stone")},
	}, 1); err != nil {
		t.Fatalf("Update to a free name: %v", err)
	}
}

// A tenant may already hold same-named vendors from before this rule. Editing
// one for any other reason must keep working — only a rename is checked.
func TestUpdate_PreExistingDuplicateCanStillBeEdited(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	a := createOrg(t, pool, uniqueName("Twin Stone"))
	b := createOrg(t, pool, uniqueName("Twin Stone Two"))
	if _, err := pool.Exec(ctx,
		`UPDATE vendor SET vendor_legal_name = $2 WHERE vendor_uuid = $1`, b.ID, a.LegalName); err != nil {
		t.Fatalf("force a duplicate name: %v", err)
	}

	if _, err := Update(ctx, pool, b.ID, UpdateVendorInput{
		vendorFields: vendorFields{LegalName: a.LegalName, Email: "twin@example.com"},
	}, 1); err != nil {
		t.Fatalf("Update of a pre-existing duplicate (name unchanged): %v", err)
	}
}
