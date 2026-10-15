//go:build dbtest

package orderform

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/dbtestutil"
)

// These run the real create and list paths against a real tenant database, so
// they are where "one transaction", "kits and items are real catalogue rows" and
// "one customer never sees another's orders" are actually proved.

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	dbtestutil.EnsureLocation(t, pool)
	return pool
}

// uniq makes a name no other test (or earlier run) has used, so catalogue rows
// are created fresh and assertions do not depend on what the database holds.
func uniq(prefix string) string {
	return fmt.Sprintf("%s %d", prefix, time.Now().UnixNano())
}

// seedPortalCustomer inserts a live CUST-stage customer with one portal login.
func seedPortalCustomer(t *testing.T, pool *pgxpool.Pool) Submitter {
	t.Helper()
	ctx := context.Background()
	var s Submitter
	err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_is_approved,
		                      customer_approval_status, customer_created_by)
		SELECT rt.record_type_id, $1, TRUE, 'approved', 1
		FROM lkp_record_type rt WHERE rt.record_type_code = 'CUST'
		RETURNING customer_id, customer_uuid::text`, uniq("Highland Homes")).Scan(&s.CustomerID, &s.CustomerUUID)
	require.NoError(t, err)
	s.FullName = "Brandon Reyes"
	s.Email = fmt.Sprintf("brandon%d@highlandhomes.test", time.Now().UnixNano())
	err = pool.QueryRow(ctx, `
		INSERT INTO customer_portal_user (identity_id, customer_id, email, full_name)
		VALUES (gen_random_uuid(), $1, $2, $3) RETURNING id::text`,
		s.CustomerID, s.Email, s.FullName).Scan(&s.PortalUserID)
	require.NoError(t, err)
	return s
}

// placeable is a valid order with one custom area whose every entry is unique.
func placeable(area string) Input {
	in := validInput()
	in.MeasureDate = time.Now().UTC().AddDate(0, 0, 2).Format(dateLayout)
	in.InstallDate = time.Now().UTC().AddDate(0, 0, 9).Format(dateLayout)
	in.Areas = []Area{{
		Kind: "custom", Name: area, Color: uniq("Serene"), Edge: uniq("Flat"), SinkModel: uniq("440185"),
		FaucetSpread: []string{},
	}}
	return in
}

type soLine struct {
	item, desc, kit string
	qty             float64
}

func salesOrderLines(t *testing.T, pool *pgxpool.Pool, soID int) []soLine {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT item_name, description, kit_name, quantity::float8
		FROM sales_order_item WHERE sales_order_id = $1 AND item_deleted_at IS NULL ORDER BY line_number`, soID)
	require.NoError(t, err)
	defer rows.Close()
	var out []soLine
	for rows.Next() {
		var l soLine
		require.NoError(t, rows.Scan(&l.item, &l.desc, &l.kit, &l.qty))
		out = append(out, l)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestCreate_StoresTheFormAndDraftsASalesOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	area := uniq("Wine Bar")
	in := placeable(area)
	in.SpecialInstructions = "Gate code 1234."

	res, err := Create(ctx, pool, who, in, time.Now().UTC())

	require.NoError(t, err)
	assert.Equal(t, StatusSubmitted, res.Form.Status)
	assert.NotEmpty(t, res.SalesOrderUUID)
	assert.Len(t, res.CreatedKits, 1)
	assert.Len(t, res.CreatedItems, 3, "colour, edge and sink model are all new")

	// The stored form, linked to the order it produced.
	var status string
	var soID *int
	var areas int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT f.order_form_status, f.sales_order_id,
		       (SELECT COUNT(*) FROM customer_order_form_area a
		         WHERE a.customer_order_form_id = f.customer_order_form_id)
		FROM customer_order_form f WHERE f.customer_order_form_uuid = $1`, res.Form.ID).Scan(&status, &soID, &areas))
	assert.Equal(t, "submitted", status)
	assert.Equal(t, 1, areas)
	require.NotNil(t, soID)

	// A Draft for the same customer, with the job site as its ship-to.
	var code, memo, shipLine1, install string
	var customerID int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT rs.record_status_code, so.sales_order_memo, so.sales_order_ship_addr_line1,
		       to_char(so.sales_order_scheduled_install_date, 'YYYY-MM-DD'), so.sales_order_customer_id
		FROM sales_order so JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		WHERE so.sales_order_id = $1`, *soID).Scan(&code, &memo, &shipLine1, &install, &customerID))
	assert.Equal(t, "DRFT", code)
	assert.Equal(t, who.CustomerID, customerID)
	assert.Equal(t, in.InstallAddress, shipLine1)
	assert.Equal(t, in.InstallDate, install)
	assert.Contains(t, memo, "Gate code 1234.")

	// One line per filled field under the area's kit, item = the entry.
	a := in.Areas[0]
	assert.Equal(t, []soLine{
		{a.Color, "Color", area, 1},
		{a.Edge, "Edge", area, 1},
		{a.SinkModel, "Sink Model", area, 1},
	}, salesOrderLines(t, pool, *soID))
}
