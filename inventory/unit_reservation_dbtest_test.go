// inventory/unit_reservation_dbtest_test.go — a slab can be held for a customer,
// and a slab that came in without a bin is only 'received' until it is put away.
//go:build dbtest

package inventory

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedReservationCustomer(t *testing.T) (uuid, name string) {
	t.Helper()
	pool := testPool(t)
	name = uniq("Reserve Customer")
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		SELECT record_type_id, $1, 1 FROM lkp_record_type WHERE record_type_code = 'CUST'
		RETURNING customer_uuid`, name).Scan(&uuid))
	return uuid, name
}

func seedReservationBin(t *testing.T) string {
	t.Helper()
	code := uniq("RSV")
	var uuid string
	require.NoError(t, testPool(t).QueryRow(context.Background(), `
		INSERT INTO inventory_bin (warehouse_id, bin_code, bin_name, bin_type, bin_path)
		VALUES (1, $1, $1, 'rack', $1) RETURNING inventory_bin_uuid`, code).Scan(&uuid))
	return uuid
}

func newReservableUnit(t *testing.T) *Unit {
	t.Helper()
	pool := testPool(t)
	item := seedAreaItem(t, pool, uniq("DBTEST-RSV"))
	u, err := CreateUnit(context.Background(), pool, CreateUnitInput{
		Serial: uniq("DBTEST-RSV"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30,
	}, 1)
	require.NoError(t, err)
	return u
}

func TestReserveUnitForCustomer_HoldsAndReleases(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	custUUID, custName := seedReservationCustomer(t)
	before := onHand(t, pool, u.InventoryItemID)

	require.NoError(t, ReserveUnitForCustomer(ctx, pool, u.ID, custUUID, "for the Smith kitchen", 1))

	got, err := GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusReserved, got.Status)
	require.NotNil(t, got.ReservedCustomerID)
	assert.Equal(t, custUUID, *got.ReservedCustomerID)
	assert.Equal(t, custName, got.ReservedCustomerName)
	assert.NotNil(t, got.CustomerReservedAt)
	assert.InDelta(t, before, onHand(t, pool, u.InventoryItemID), 1e-9, "a reserved slab is still on hand")

	// Held already: a second reservation is refused, not stacked.
	err = ReserveUnitForCustomer(ctx, pool, u.ID, custUUID, "", 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "already reserved")

	require.NoError(t, ReleaseUnitReservation(ctx, pool, u.ID, "", 1))
	got, err = GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAvailable, got.Status)
	assert.Nil(t, got.ReservedCustomerID)
	assert.Empty(t, got.ReservedCustomerName)
}

func TestReserveUnitForCustomer_RefusesWhatCannotBeReserved(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	custUUID, _ := seedReservationCustomer(t)

	for _, status := range []string{StatusReceived, StatusOnOrder, StatusInTransit, StatusScrapped} {
		u := newReservableUnit(t)
		_, err := pool.Exec(ctx, `UPDATE inventory_slab SET slab_status = $2 WHERE inventory_slab_uuid = $1`, u.ID, status)
		require.NoError(t, err)
		err = ReserveUnitForCustomer(ctx, pool, u.ID, custUUID, "", 1)
		require.Error(t, err, status)
		assert.True(t, IsClientError(err), "%s must be a 400: %v", status, err)
	}

	u := newReservableUnit(t)
	err := ReserveUnitForCustomer(ctx, pool, u.ID, "00000000-0000-0000-0000-000000000000", "", 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err), "an unknown customer is a 400")
	got, err := GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAvailable, got.Status, "a refused reservation changes nothing")
}

func TestReserveUnitForCustomer_OnlyActiveCustomers(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	custUUID, _ := seedReservationCustomer(t)
	_, err := pool.Exec(ctx, `
		UPDATE customer SET customer_crm_status = (SELECT crm_status_id FROM lkp_crm_status WHERE crm_status_code = 'CINA')
		WHERE customer_uuid = $1`, custUUID)
	require.NoError(t, err)

	err = ReserveUnitForCustomer(ctx, pool, u.ID, custUUID, "", 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "Only Active customers")
}

func TestReleaseUnitReservation_RefusesAJobsReservation(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	// Reserved by a fabrication job: status reserved, but no customer on the slab.
	_, err := pool.Exec(ctx, `UPDATE inventory_slab SET slab_status = 'reserved' WHERE inventory_slab_uuid = $1`, u.ID)
	require.NoError(t, err)

	err = ReleaseUnitReservation(ctx, pool, u.ID, "", 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "fabrication job")
}

func TestScrapUnit_ClearsACustomerReservation(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	custUUID, _ := seedReservationCustomer(t)
	require.NoError(t, ReserveUnitForCustomer(ctx, pool, u.ID, custUUID, "", 1))

	require.NoError(t, ScrapUnit(ctx, pool, u.ID, nil, "cracked", 1))

	var left *int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT reserved_customer_id FROM inventory_slab WHERE inventory_slab_uuid = $1`, u.ID).Scan(&left))
	assert.Nil(t, left, "a written-off slab holds nobody's reservation")
	got, err := GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Nil(t, got.ReservedCustomerID)
}

func TestReservedUnit_CannotBeCutOrTransferred(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	custUUID, _ := seedReservationCustomer(t)
	require.NoError(t, ReserveUnitForCustomer(ctx, pool, u.ID, custUUID, "", 1))

	_, err := CutUnit(ctx, pool, u.ID, CutInput{}, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "reserved")
}

func TestReceivedUnit_BecomesAvailableOnceItIsPutInABin(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	_, err := pool.Exec(ctx, `UPDATE inventory_slab SET slab_status = 'received' WHERE inventory_slab_uuid = $1`, u.ID)
	require.NoError(t, err)
	bin := seedReservationBin(t)

	require.NoError(t, MoveUnitToBin(ctx, pool, u.ID, MoveUnitInput{BinUUID: &bin}, 1))

	got, err := GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAvailable, got.Status, "placing a received slab in a bin puts it away")
	require.NotNil(t, got.BinID)
}

func TestReceivedUnit_CountsAsStockAndCanBeScrapped(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	_, err := pool.Exec(ctx, `UPDATE inventory_slab SET slab_status = 'received' WHERE inventory_slab_uuid = $1`, u.ID)
	require.NoError(t, err)
	require.Greater(t, onHand(t, pool, u.InventoryItemID), 0.0)

	require.NoError(t, ScrapUnit(ctx, pool, u.ID, nil, "dropped off the truck", 1))

	assert.InDelta(t, 0.0, onHand(t, pool, u.InventoryItemID), 1e-9, "scrapping a received slab takes it out of stock")
	assert.InDelta(t, 0.0, ledgerSum(t, pool, u.InventoryItemID), 1e-9)
}
