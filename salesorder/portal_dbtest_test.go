// salesorder/portal_dbtest_test.go
//go:build dbtest

package salesorder

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// These are the tests that matter most for the customer portal: they prove,
// against a real database and through the real store functions, that one
// customer cannot reach another customer's documents and that unfinished
// documents never appear.
//
// The unit tests in portal_search_test.go assert the predicate's SHAPE; these
// assert its EFFECT.

func portalTestPool(t *testing.T) *pgxpool.Pool {
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

// seedApprovedCustomer inserts a live, approved CUST-stage customer.
func seedApprovedCustomer(t *testing.T, pool *pgxpool.Pool, name string) int {
	t.Helper()
	ctx := context.Background()
	var id int
	err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_is_approved,
		                      customer_approval_status, customer_created_by)
		SELECT rt.record_type_id, $1, TRUE, 'approved', 1
		FROM lkp_record_type rt WHERE rt.record_type_code = 'CUST'
		RETURNING customer_id`,
		fmt.Sprintf("%s-%d", name, time.Now().UnixNano())).Scan(&id)
	if err != nil {
		t.Fatalf("seed customer %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM customer WHERE customer_id = $1`, id)
	})
	return id
}

// seedSalesOrder inserts one invoice for a customer in the given status code.
func seedSalesOrder(t *testing.T, pool *pgxpool.Pool, customerID int, statusCode string, total float64) string {
	t.Helper()
	ctx := context.Background()
	// sales_order_number is set explicitly: the real create path generates it in Go
	// after insert, and the column is nullable, but scanInvoice reads it into a
	// non-pointer string. Every real row has one.
	number := fmt.Sprintf("SO-%d", time.Now().UnixNano()%100000000)
	var uuid string
	err := pool.QueryRow(ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id,
		                     sales_order_created_by, sales_order_grand_total, sales_order_number)
		SELECT rt.record_type_id, rs.record_status_id, $1, 1, $3, $4
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id
		WHERE rt.record_type_code = 'SORD' AND rs.record_status_code = $2
		RETURNING sales_order_uuid`, customerID, statusCode, total, number).Scan(&uuid)
	if err != nil {
		t.Fatalf("seed %s sales order: %v", statusCode, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM sales_order WHERE sales_order_uuid = $1`, uuid)
	})
	return uuid
}

// A portal customer must never see another customer's invoices, and must never
// see its own unfinished ones.
func TestPortalSearch_IsolatesCustomerAndHidesDrafts(t *testing.T) {
	pool := portalTestPool(t)
	ctx := context.Background()

	acme := seedApprovedCustomer(t, pool, "Acme Stone")
	borealis := seedApprovedCustomer(t, pool, "Borealis Marble")

	// Acme: one invoice in every interesting state.
	visible := map[string]string{}
	for _, code := range []string{"APPV", "OPEN", "PART", "FILL", "CANC"} {
		visible[code] = seedSalesOrder(t, pool, acme, code, 100)
	}
	hidden := map[string]string{}
	for _, code := range []string{"DRFT", "PAPV"} {
		hidden[code] = seedSalesOrder(t, pool, acme, code, 100)
	}
	// Borealis: a SENT invoice that Acme must never see.
	borealisUUID := seedSalesOrder(t, pool, borealis, "APPV", 999)

	page, err := PortalSearch(ctx, pool, acme, query.Request{Limit: 100})
	if err != nil {
		t.Fatalf("PortalSearch: %v", err)
	}

	got := map[string]bool{}
	for _, rec := range page.Records {
		got[rec.ID] = true
	}

	for code, uuid := range visible {
		if !got[uuid] {
			t.Errorf("%s sales order missing from the portal view; it should be visible", code)
		}
	}
	for code, uuid := range hidden {
		if got[uuid] {
			t.Errorf("%s sales order is visible to the customer; unfinished documents must be hidden", code)
		}
	}
	if got[borealisUUID] {
		t.Error("SECURITY: another customer's sales order appeared in the portal view")
	}
	if len(page.Records) != len(visible) {
		t.Errorf("portal view returned %d records, want %d", len(page.Records), len(visible))
	}
}

// A caller-supplied customer_id filter must only narrow. Naming another
// customer must yield nothing, not that customer's records.
func TestPortalSearch_HostileCustomerFilterCannotWiden(t *testing.T) {
	pool := portalTestPool(t)
	ctx := context.Background()

	acme := seedApprovedCustomer(t, pool, "Acme Stone")
	borealis := seedApprovedCustomer(t, pool, "Borealis Marble")
	seedSalesOrder(t, pool, acme, "APPV", 100)
	seedSalesOrder(t, pool, borealis, "APPV", 999)

	page, err := PortalSearch(ctx, pool, acme, query.Request{
		Limit: 100,
		Filters: []query.Clause{
			{Field: "customer_id", Op: "eq", Value: fmt.Sprintf("%d", borealis)},
		},
	})
	if err != nil {
		t.Fatalf("PortalSearch with filter: %v", err)
	}
	if len(page.Records) != 0 {
		t.Errorf("SECURITY: filtering for another customer returned %d records, want 0",
			len(page.Records))
	}
}

// PortalGet must refuse another customer's document and any hidden document,
// and must report both as not-found so ids cannot be probed.
func TestPortalGet_RefusesOtherCustomerAndHiddenStates(t *testing.T) {
	pool := portalTestPool(t)
	ctx := context.Background()

	acme := seedApprovedCustomer(t, pool, "Acme Stone")
	borealis := seedApprovedCustomer(t, pool, "Borealis Marble")

	ownSent := seedSalesOrder(t, pool, acme, "APPV", 100)
	ownDraft := seedSalesOrder(t, pool, acme, "DRFT", 100)
	othersSent := seedSalesOrder(t, pool, borealis, "APPV", 999)

	if _, err := PortalGet(ctx, pool, acme, ownSent); err != nil {
		t.Fatalf("PortalGet on own visible sales order: %v", err)
	}

	for name, uuid := range map[string]string{
		"own hidden-state sales order":   ownDraft,
		"another customer's sales order": othersSent,
	} {
		_, err := PortalGet(ctx, pool, acme, uuid)
		if err != ErrNotFound {
			t.Errorf("PortalGet on %s: err = %v, want ErrNotFound (404, never 403)", name, err)
		}
	}
}

// The detail payload must carry enough to render the printed document, since
// the frontend generates the PDF from it.
func TestPortalGet_ReturnsFullBodyForRendering(t *testing.T) {
	pool := portalTestPool(t)
	ctx := context.Background()

	acme := seedApprovedCustomer(t, pool, "Acme Stone")
	uuid := seedSalesOrder(t, pool, acme, "APPV", 250)

	rec, err := PortalGet(ctx, pool, acme, uuid)
	if err != nil {
		t.Fatalf("PortalGet: %v", err)
	}
	if rec.ID != uuid {
		t.Errorf("ID = %q, want %q", rec.ID, uuid)
	}
	if rec.Items == nil {
		t.Error("Items is nil; the detail payload must carry line items for PDF rendering")
	}
	if rec.GrandTotal == 0 {
		t.Error("GrandTotal is zero; totals must be present for PDF rendering")
	}
	if rec.Customer.Name == "" {
		t.Error("Customer name missing; required on the printed document")
	}
}
