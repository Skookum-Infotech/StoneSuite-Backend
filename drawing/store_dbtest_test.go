// drawing/store_dbtest_test.go
//go:build dbtest

package drawing

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

func seedCustomer(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	var id int
	err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_is_approved,
		                      customer_approval_status, customer_created_by)
		SELECT rt.record_type_id, $1, TRUE, 'approved', 1
		FROM lkp_record_type rt WHERE rt.record_type_code = 'CUST'
		RETURNING customer_id`,
		fmt.Sprintf("Drawing Test Customer-%d", time.Now().UnixNano())).Scan(&id)
	if err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM customer WHERE customer_id = $1`, id)
	})
	return id
}

// seedSalesOrderID inserts a minimal sales order (any seeded SORD status --
// this package's tests don't care which) and returns its integer id, the
// column sales_order_drawing/sales_order_drawing_version actually FK to.
func seedSalesOrderID(t *testing.T, pool *pgxpool.Pool, customerID int) int {
	t.Helper()
	ctx := context.Background()
	number := fmt.Sprintf("SO-%d", time.Now().UnixNano()%100000000)
	var id int
	err := pool.QueryRow(ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id,
		                     sales_order_created_by, sales_order_grand_total, sales_order_number)
		SELECT rt.record_type_id,
		       (SELECT record_status_id FROM lkp_record_status
		        WHERE record_status_record_type = rt.record_type_id ORDER BY record_status_id LIMIT 1),
		       $1, 1, 0, $2
		FROM lkp_record_type rt WHERE rt.record_type_code = 'SORD'
		RETURNING sales_order_id`, customerID, number).Scan(&id)
	if err != nil {
		t.Fatalf("seed sales order: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sales_order WHERE sales_order_id = $1`, id)
	})
	return id
}

func validRectArea() Area {
	return Area{ID: "a1", Name: "Area 1", Pieces: []Piece{
		{ID: "p1", Label: "A-1", Shape: "rect", Outline: validRectOutline, ThicknessMm: 30},
	}}
}

func validDrawing() Drawing {
	return Drawing{DisplayUnit: "in", FractionDenominator: 32, Areas: []Area{validRectArea()}}
}

func TestSaveAndGet_RoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))

	saved, err := Save(ctx, pool, soID, validDrawing(), nil, nil, "Test User")
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Version != 1 {
		t.Fatalf("Version = %d, want 1", saved.Version)
	}

	got, err := Get(ctx, pool, soID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Version != saved.Version {
		t.Errorf("Get().Version = %d, want %d", got.Version, saved.Version)
	}
	if len(got.Areas) != 1 || len(got.Areas[0].Pieces) != 1 {
		t.Errorf("Get().Areas = %+v, want 1 area with 1 piece", got.Areas)
	}
}

func TestGet_NoDrawingYet_ReturnsErrNotFound(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))

	_, err := Get(ctx, pool, soID)
	if err == nil {
		t.Fatal("Get() error = nil, want ErrNotFound")
	}
}

func TestSave_StaleVersionConflicts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))

	first, err := Save(ctx, pool, soID, validDrawing(), nil, nil, "A")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	second, err := Save(ctx, pool, soID, validDrawing(), &first.Version, nil, "B")
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if second.Version != 2 {
		t.Fatalf("second Save().Version = %d, want 2", second.Version)
	}

	stale := first.Version // 1, but current is now 2
	_, err = Save(ctx, pool, soID, validDrawing(), &stale, nil, "C")
	if err == nil {
		t.Fatal("Save() with a stale version error = nil, want ErrVersionConflict")
	}
}

func TestRestore_CreatesNewVersionFromOldSnapshot(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))

	onePieceDrawing := validDrawing()
	twoPieceDrawing := validDrawing()
	twoPieceDrawing.Areas[0].Pieces = append(twoPieceDrawing.Areas[0].Pieces, Piece{
		ID: "p2", Label: "A-2", Shape: "rect", Outline: validRectOutline, ThicknessMm: 30,
	})

	v1, err := Save(ctx, pool, soID, onePieceDrawing, nil, nil, "A")
	if err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	v2, err := Save(ctx, pool, soID, twoPieceDrawing, &v1.Version, nil, "A")
	if err != nil {
		t.Fatalf("Save v2: %v", err)
	}

	restored, err := Restore(ctx, pool, soID, v1.Version, &v2.Version, nil, "B")
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if restored.Version != 3 {
		t.Errorf("Restore().Version = %d, want 3 (a new version, not a rewind)", restored.Version)
	}
	if len(restored.Areas[0].Pieces) != 1 {
		t.Errorf("Restore().Areas[0].Pieces has %d pieces, want 1 (v1's content)", len(restored.Areas[0].Pieces))
	}

	items, _, err := ListVersions(ctx, pool, soID, "", 10)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(items) != 3 {
		t.Errorf("ListVersions() returned %d items, want 3", len(items))
	}

	fetched, err := GetVersion(ctx, pool, soID, v1.Version)
	if err != nil {
		t.Fatalf("GetVersion: %v", err)
	}
	if len(fetched.Areas[0].Pieces) != 1 {
		t.Errorf("GetVersion(v1).Areas[0].Pieces has %d pieces, want 1", len(fetched.Areas[0].Pieces))
	}
}

// actorID is an employee that exists in every seeded tenant DB (the other
// tests here use it as created_by); coalescing only ever applies to a known actor.
const testActorID = 1

func versionCount(t *testing.T, pool *pgxpool.Pool, soID int) int {
	t.Helper()
	items, _, err := ListVersions(context.Background(), pool, soID, "", 50)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	return len(items)
}

// ageLatestVersion back-dates the newest history row so the next save sees it
// as older than the coalescing window (idleBy) and/or the burst-span cap (spanBy).
func ageLatestVersion(t *testing.T, pool *pgxpool.Pool, soID int, idleBy, spanBy string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		UPDATE sales_order_drawing_version
		SET at = at - $2::text::interval, first_at = COALESCE(first_at, at) - $3::text::interval
		WHERE sales_order_id = $1
		  AND version = (SELECT MAX(version) FROM sales_order_drawing_version WHERE sales_order_id = $1)`,
		soID, idleBy, spanBy)
	if err != nil {
		t.Fatalf("age latest version: %v", err)
	}
}

func TestSave_RapidSavesBySamePersonFoldIntoOneVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))
	actor := testActorID

	var cur *Drawing
	var expected *int
	for i := 0; i < 3; i++ {
		d := validDrawing()
		d.Areas[0].Name = fmt.Sprintf("Edit %d", i+1)
		saved, err := Save(ctx, pool, soID, d, expected, &actor, "Test User")
		if err != nil {
			t.Fatalf("Save #%d: %v", i+1, err)
		}
		cur, expected = saved, &saved.Version
	}

	if cur.Version != 3 {
		t.Errorf("record version = %d, want 3 (it still increments on every save)", cur.Version)
	}
	items, _, err := ListVersions(ctx, pool, soID, "", 50)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(items) != 1 || items[0].Version != 3 {
		t.Fatalf("history = %+v, want exactly one entry labelled version 3", items)
	}
	snap, err := GetVersion(ctx, pool, soID, 3)
	if err != nil {
		t.Fatalf("GetVersion(3): %v", err)
	}
	if snap.Areas[0].Name != "Edit 3" {
		t.Errorf("history snapshot area name = %q, want the latest content %q", snap.Areas[0].Name, "Edit 3")
	}
}

func TestSave_ConflictStillDetectedAfterFolding(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))
	actor := testActorID

	first, err := Save(ctx, pool, soID, validDrawing(), nil, &actor, "A")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if _, err := Save(ctx, pool, soID, validDrawing(), &first.Version, &actor, "A"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	// A second tab still holding version 1 must be rejected even though the
	// history row it was based on has been folded forward.
	_, err = Save(ctx, pool, soID, validDrawing(), &first.Version, &actor, "A")
	if !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("Save() with a stale version error = %v, want ErrVersionConflict", err)
	}
}

func TestSave_UnknownActorNeverFolds(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))

	first, err := Save(ctx, pool, soID, validDrawing(), nil, nil, "Anon")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if _, err := Save(ctx, pool, soID, validDrawing(), &first.Version, nil, "Anon"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if got := versionCount(t, pool, soID); got != 2 {
		t.Errorf("history has %d entries, want 2 (no actor to attribute a fold to)", got)
	}
}

func TestSave_DifferentActorDoesNotFold(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))
	actor := testActorID

	first, err := Save(ctx, pool, soID, validDrawing(), nil, &actor, "A")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// nil is "someone other than the actor who wrote the previous row".
	if _, err := Save(ctx, pool, soID, validDrawing(), &first.Version, nil, "B"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if got := versionCount(t, pool, soID); got != 2 {
		t.Errorf("history has %d entries, want 2 (another person's edit is its own version)", got)
	}
}

func TestSave_AfterIdleWindowStartsNewVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))
	actor := testActorID

	first, err := Save(ctx, pool, soID, validDrawing(), nil, &actor, "A")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	ageLatestVersion(t, pool, soID, "2 hours", "2 hours")
	if _, err := Save(ctx, pool, soID, validDrawing(), &first.Version, &actor, "A"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if got := versionCount(t, pool, soID); got != 2 {
		t.Errorf("history has %d entries, want 2 (the earlier burst ended long ago)", got)
	}
}

func TestSave_AfterMaxSpanStartsNewVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))
	actor := testActorID

	first, err := Save(ctx, pool, soID, validDrawing(), nil, &actor, "A")
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	// Last write was just now, but the burst began 2 hours ago: past the span cap.
	ageLatestVersion(t, pool, soID, "0 seconds", "2 hours")
	if _, err := Save(ctx, pool, soID, validDrawing(), &first.Version, &actor, "A"); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if got := versionCount(t, pool, soID); got != 2 {
		t.Errorf("history has %d entries, want 2 (a long session must still leave restore points)", got)
	}
}

func TestRestore_NeverFoldsIntoPreviousVersion(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	soID := seedSalesOrderID(t, pool, seedCustomer(t, pool))
	actor := testActorID

	v1, err := Save(ctx, pool, soID, validDrawing(), nil, &actor, "A")
	if err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	ageLatestVersion(t, pool, soID, "2 hours", "2 hours") // keep v1 as its own restore point
	changed := validDrawing()
	changed.Areas[0].Name = "Changed"
	v2, err := Save(ctx, pool, soID, changed, &v1.Version, &actor, "A")
	if err != nil {
		t.Fatalf("Save v2: %v", err)
	}
	if _, err := Restore(ctx, pool, soID, v1.Version, &v2.Version, &actor, "A"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := versionCount(t, pool, soID); got != 3 {
		t.Errorf("history has %d entries, want 3 (restore must add its own entry, not overwrite the one before it)", got)
	}
}
