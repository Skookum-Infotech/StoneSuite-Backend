//go:build dbtest

package reports

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
)

func dbPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestRewrittenReportQueriesRunAgainstTheRealSchema executes every report whose
// SQL was changed to take the tenant-local date and to total over all rows, so
// a typo in a column, alias or placeholder fails here rather than in production.
func TestRewrittenReportQueriesRunAgainstTheRealSchema(t *testing.T) {
	pool := dbPool(t)
	ctx := context.Background()
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)

	if _, _, err := APAging(ctx, pool, authz.ScopeAll, "", today); err != nil {
		t.Errorf("APAging: %v", err)
	}
	if _, _, err := OpenPurchaseOrders(ctx, pool, authz.ScopeAll, "", today); err != nil {
		t.Errorf("OpenPurchaseOrders: %v", err)
	}
	if _, _, err := PurchasesByVendor(ctx, pool, PurchaseFilter{Scope: authz.ScopeAll}); err != nil {
		t.Errorf("PurchasesByVendor: %v", err)
	}
	if _, _, err := PurchasesByVendor(ctx, pool, PurchaseFilter{Scope: authz.ScopeAll, From: today.AddDate(-1, 0, 0), To: today}); err != nil {
		t.Errorf("PurchasesByVendor (windowed): %v", err)
	}
	if _, _, err := UpcomingInstalls(ctx, pool, 8, authz.ScopeAll, "", today); err != nil {
		t.Errorf("UpcomingInstalls: %v", err)
	}
	if _, err := StaleRecords(ctx, pool, 14, authz.ScopeAll, "", today); err != nil {
		t.Errorf("StaleRecords: %v", err)
	}
}
