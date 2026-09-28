//go:build dbtest

package crmstore

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/duplicate"
	"stonesuite-backend/workflow"
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

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s %d", prefix, time.Now().UnixNano())
}

func coreFor(name string) map[string]any {
	return map[string]any{"customer_name": name, "customer_contact_email": "ap@example.com"}
}

func createRecord(t *testing.T, pool *pgxpool.Pool, key, name string) *workflow.Record {
	t.Helper()
	rec, err := For("v2").CreateRecord(context.Background(), pool, key, CreateInput{CoreFields: coreFor(name)})
	if err != nil {
		t.Fatalf("CreateRecord(%s, %q): %v", key, name, err)
	}
	return rec
}

func requireDuplicate(t *testing.T, err error, wantID string) *duplicate.Error {
	t.Helper()
	d, ok := duplicate.As(err)
	if !ok {
		t.Fatalf("err = %v, want a *duplicate.Error", err)
	}
	if d.Entity != duplicate.EntityCustomer {
		t.Errorf("Entity = %q, want %q", d.Entity, duplicate.EntityCustomer)
	}
	if d.ExistingID != wantID {
		t.Errorf("ExistingID = %q, want %q", d.ExistingID, wantID)
	}
	return d
}

func TestCreateRecord_CustomerNameIsUnique_WhateverTheCaseOrSpacing(t *testing.T) {
	pool := testPool(t)
	name := uniqueName("Granite Works")
	first := createRecord(t, pool, "customer", name)

	for _, variant := range []string{name, strings.ToUpper(name), "  " + strings.ToLower(name) + " ", strings.Replace(name, " ", "   ", 1)} {
		_, err := For("v2").CreateRecord(context.Background(), pool, "customer", CreateInput{CoreFields: coreFor(variant)})
		d := requireDuplicate(t, err, first.ID)
		if d.ExistingStatus == "" {
			t.Errorf("ExistingStatus is empty, want the record's status name so a client can say it is not usable")
		}
	}
}

// Two leads at one company are ordinary, and a prospect keeps the name of the
// customer it becomes — only the customer stage's name is unique.
func TestCreateRecord_LeadsAndProspectsMayShareACustomersName(t *testing.T) {
	pool := testPool(t)
	name := uniqueName("Shared Name Co")

	createRecord(t, pool, "lead", name)
	createRecord(t, pool, "lead", name)
	createRecord(t, pool, "prospect", name)
	createRecord(t, pool, "customer", name)
}

func TestCreateRecord_DeletedCustomerFreesTheName(t *testing.T) {
	pool := testPool(t)
	name := uniqueName("Closed Account Co")
	first := createRecord(t, pool, "customer", name)
	if err := For("v2").DeleteRecord(context.Background(), pool, first.ID); err != nil {
		t.Fatalf("DeleteRecord: %v", err)
	}

	createRecord(t, pool, "customer", name)
}

func TestUpdateRecord_RenameOntoAnExistingCustomerIsRejected(t *testing.T) {
	pool := testPool(t)
	taken := createRecord(t, pool, "customer", uniqueName("Taken Co"))
	other := createRecord(t, pool, "customer", uniqueName("Other Co"))

	err := For("v2").UpdateRecord(context.Background(), pool, other.ID,
		map[string]any{"customer_name": strings.ToUpper(taken.CoreFields["customer_name"].(string))}, nil)
	requireDuplicate(t, err, taken.ID)
}

func TestUpdateRecord_KeepingTheNameOrRenamingToAFreeOneIsAllowed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	rec := createRecord(t, pool, "customer", uniqueName("Steady Co"))
	name := rec.CoreFields["customer_name"].(string)

	if err := For("v2").UpdateRecord(ctx, pool, rec.ID, map[string]any{"customer_name": name, "customer_dba_name": "Steady DBA"}, nil); err != nil {
		t.Fatalf("UpdateRecord keeping the same name: %v", err)
	}
	if err := For("v2").UpdateRecord(ctx, pool, rec.ID, map[string]any{"customer_name": uniqueName("Renamed Co")}, nil); err != nil {
		t.Fatalf("UpdateRecord to a free name: %v", err)
	}
}

// A tenant may already hold same-named customers from before this rule.
// Editing one for any other reason must keep working — only a rename is checked.
func TestUpdateRecord_PreExistingDuplicateCanStillBeEdited(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	a := createRecord(t, pool, "customer", uniqueName("Twin Co"))
	b := createRecord(t, pool, "customer", uniqueName("Twin Co Two"))
	name := a.CoreFields["customer_name"].(string)
	if _, err := pool.Exec(ctx, `UPDATE customer SET customer_name = $2 WHERE customer_uuid = $1`, b.ID, name); err != nil {
		t.Fatalf("force a duplicate name: %v", err)
	}

	if err := For("v2").UpdateRecord(ctx, pool, b.ID, map[string]any{"customer_name": name, "customer_dba_name": "Twin DBA"}, nil); err != nil {
		t.Fatalf("UpdateRecord of a pre-existing duplicate (name unchanged): %v", err)
	}
}

// A prospect that is really an existing customer must not be converted into a
// second one.
func TestConvertRecord_IntoAnExistingCustomersNameIsRejected(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	name := uniqueName("Already A Customer")
	prospect := createRecord(t, pool, "prospect", name)
	existing := createRecord(t, pool, "customer", name)
	if _, err := pool.Exec(ctx, `
		UPDATE customer SET customer_crm_status = (
			SELECT cs.crm_status_id FROM lkp_crm_status cs
			WHERE cs.crm_status_record_type = customer.record_type AND cs.crm_status_code = $2)
		WHERE customer_uuid = $1`, prospect.ID, statusProspectPendingConversion); err != nil {
		t.Fatalf("move the prospect to Pending Conversion: %v", err)
	}

	_, _, _, err := For("v2").ConvertRecord(ctx, pool, prospect.ID, "customer", nil, nil, "")
	requireDuplicate(t, err, existing.ID)
}
