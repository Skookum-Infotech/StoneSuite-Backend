// crmactivity/store_test.go
//go:build dbtest

package crmactivity

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

// seedCustomer inserts a minimal live customer, mirroring the seed helpers
// in quote/estimate/salesorder/invoice's store_test.go files.
func seedCustomer(t *testing.T, pool *pgxpool.Pool) (custUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`,
		custTypeID, "Test Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return custUUID
}

func TestCreate_LogsActivity(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)

	in := CreateActivityInput{}
	in.ActivityType = "call"
	in.Subject = "Intro call"
	in.Body = "Discussed pricing."
	got, err := Create(context.Background(), pool, custUUID, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.RecordID != custUUID {
		t.Errorf("RecordID = %q, want %q", got.RecordID, custUUID)
	}
	if got.ActivityType != "call" {
		t.Errorf("ActivityType = %q, want call", got.ActivityType)
	}
	if got.Subject != "Intro call" {
		t.Errorf("Subject = %q, want %q", got.Subject, "Intro call")
	}
}

func TestCreate_RejectsInvalidType(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)

	in := CreateActivityInput{}
	in.ActivityType = "carrier-pigeon"
	_, err := Create(context.Background(), pool, custUUID, in, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with invalid type = %v, want ClientError", err)
	}
}

func TestCreate_UnknownRecord(t *testing.T) {
	pool := testPool(t)
	in := CreateActivityInput{}
	in.ActivityType = "note"
	_, err := Create(context.Background(), pool, "00000000-0000-0000-0000-000000000000", in, 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Create against unknown record: err = %v, want ErrNotFound", err)
	}
}

func TestList_FiltersByTypeAndOrdersRecent(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)

	call := CreateActivityInput{}
	call.ActivityType = "call"
	call.Subject = "Call 1"
	if _, err := Create(context.Background(), pool, custUUID, call, 1); err != nil {
		t.Fatalf("seed call: %v", err)
	}
	note := CreateActivityInput{}
	note.ActivityType = "note"
	note.Subject = "Note 1"
	if _, err := Create(context.Background(), pool, custUUID, note, 1); err != nil {
		t.Fatalf("seed note: %v", err)
	}

	all, err := List(context.Background(), pool, custUUID, "")
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("len(all) = %d, want 2", len(all))
	}

	calls, err := List(context.Background(), pool, custUUID, "call")
	if err != nil {
		t.Fatalf("List calls: %v", err)
	}
	if len(calls) != 1 || calls[0].ActivityType != "call" {
		t.Fatalf("List(type=call) = %+v, want exactly 1 call", calls)
	}
}

func TestUpdateAndDelete_RoundTrip(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)

	in := CreateActivityInput{}
	in.ActivityType = "task"
	in.Subject = "Follow up"
	created, err := Create(context.Background(), pool, custUUID, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	upd := UpdateActivityInput{}
	upd.ActivityType = "task"
	upd.Subject = "Follow up (done)"
	updated, err := Update(context.Background(), pool, custUUID, created.ID, upd, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Subject != "Follow up (done)" {
		t.Errorf("Subject = %q, want %q", updated.Subject, "Follow up (done)")
	}

	if err := SoftDelete(context.Background(), pool, custUUID, created.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := Get(context.Background(), pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete: err = %v, want ErrNotFound", err)
	}
}

func TestUpdate_RejectsMismatchedParent(t *testing.T) {
	pool := testPool(t)
	custUUID := seedCustomer(t, pool)
	otherCustUUID := seedCustomer(t, pool)

	in := CreateActivityInput{}
	in.ActivityType = "note"
	created, err := Create(context.Background(), pool, custUUID, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	upd := UpdateActivityInput{}
	upd.ActivityType = "note"
	upd.Subject = "hijacked"
	_, err = Update(context.Background(), pool, otherCustUUID, created.ID, upd, 1)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Update via wrong parent: err = %v, want ErrNotFound", err)
	}
}
