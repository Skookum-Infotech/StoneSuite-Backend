//go:build dbtest

package docextractjob

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const (
	ownerA = "identity-owner-a"
	ownerB = "identity-owner-b"
)

// testPool connects to TEST_DATABASE_URL (skipping when unset) and clears the
// document_* tables so each test starts clean.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(context.Background(),
		`TRUNCATE document_extractions, document_party_alias, document_item_alias, document_extraction_feedback`)
	require.NoError(t, err)
	return pool
}

// uniq returns a digits-only token unique to this call, appended to a name's
// first word so rows left by earlier runs never share a prefilter token.
func uniq() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

// seedCustomer inserts a live CUST customer with the given CRM status code
// ("CACT" is Active) and returns its uuid.
func seedCustomer(t *testing.T, pool *pgxpool.Pool, name, statusCode string) string {
	t.Helper()
	var u string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO customer (record_type, customer_name, customer_crm_status, customer_created_by)
		SELECT rt.record_type_id, $1, cs.crm_status_id, 1
		FROM lkp_record_type rt, lkp_crm_status cs
		WHERE rt.record_type_code = 'CUST' AND cs.crm_status_code = $2
		LIMIT 1
		RETURNING customer_uuid::text`, name, statusCode).Scan(&u)
	require.NoError(t, err, "seed customer %q", name)
	return u
}

// seedItem inserts a live inventory item in the unit with unitCode.
func seedItem(t *testing.T, pool *pgxpool.Pool, sku, name, unitCode string, priceCents int64, active bool) string {
	t.Helper()
	var u string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id,
		                            inventory_item_unit_price, inventory_item_is_active)
		SELECT $1, $2, u.unit_id, $3::numeric / 100, $4 FROM lkp_unit u WHERE u.unit_code = $5
		RETURNING inventory_item_uuid::text`, sku, name, priceCents, active, unitCode).Scan(&u)
	require.NoError(t, err, "seed item %q", sku)
	return u
}

// seedSalesOrder inserts a live sales order (status code e.g. "OPEN", "FILL").
func seedSalesOrder(t *testing.T, pool *pgxpool.Pool, custUUID, number, po, statusCode string, totalCents int64) string {
	t.Helper()
	var u string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id, sales_order_number,
		                         sales_order_po_number, sales_order_subtotal, sales_order_grand_total, sales_order_created_by)
		SELECT rt.record_type_id, rs.record_status_id, c.customer_id, $2, $3, $4::numeric / 100, $4::numeric / 100, 1
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id AND rs.record_status_code = $5
		JOIN customer c ON c.customer_uuid = $1::uuid
		WHERE rt.record_type_code = 'SORD'
		RETURNING sales_order_uuid::text`, custUUID, number, po, totalCents, statusCode).Scan(&u)
	require.NoError(t, err, "seed sales order %q", number)
	return u
}

// newExtraction creates a Store extraction for owner with a fixed 24h TTL.
func newExtraction(t *testing.T, s *Store, owner string) *Extraction {
	t.Helper()
	e, err := s.Create(context.Background(), CreateParams{
		DocType: "sales_order", OwnerIdentityID: owner, FileName: "po.pdf", ContentType: "application/pdf",
		SizeBytes: 1000, TenantSlug: "acme", Extension: "pdf", TTL: 24 * time.Hour,
	})
	require.NoError(t, err)
	return e
}
