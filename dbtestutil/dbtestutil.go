// Package dbtestutil holds fixtures shared by the DB-backed (-tags dbtest and
// TEST_DATABASE_URL) tests. It is imported only from _test files.
package dbtestutil

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// EnsureLocation guarantees the test database has the location inventory tests
// assume: company_location_id = 1, live, and the tenant's default when no other
// live location already is.
//
// Inventory holds stock at a company location (every warehouse_id column is a
// foreign key to company_location), and the schema no longer seeds one -- a
// tenant's locations come from Configuration -> Company Info. Tests written when
// a "Main Warehouse" was always present as id 1 keep using WarehouseID: 1, and
// this is what makes that id real again. Idempotent, and it advances the SERIAL
// past id 1 so a later Create in the same database cannot collide with it.
func EnsureLocation(t testing.TB, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO company_location (company_location_id, name, is_default)
		SELECT 1, 'Test Location',
		       NOT EXISTS (SELECT 1 FROM company_location WHERE is_default AND deleted_at IS NULL)
		WHERE NOT EXISTS (SELECT 1 FROM company_location WHERE company_location_id = 1)`); err != nil {
		t.Fatalf("seed test location: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		SELECT setval(pg_get_serial_sequence('company_location', 'company_location_id'),
		              GREATEST((SELECT COALESCE(MAX(company_location_id), 1) FROM company_location),
		                       (SELECT last_value FROM company_location_company_location_id_seq)))`); err != nil {
		t.Fatalf("advance company_location sequence: %v", err)
	}
}
