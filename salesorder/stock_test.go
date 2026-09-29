// salesorder/stock_test.go
//go:build dbtest

package salesorder

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
)

// ---- fixtures ----------------------------------------------------------------

func uniqSuffix() string { return fmt.Sprintf("%d", time.Now().UnixNano()) }

// giveStock puts qty on hand for an item at warehouse 1 (replacing any).
func giveStock(t *testing.T, pool *pgxpool.Pool, itemUUID string, qty float64) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		SELECT inventory_item_id, 1, $2 FROM inventory_item WHERE inventory_item_uuid = $1
		ON CONFLICT (inventory_item_id, warehouse_id) DO UPDATE SET quantity_on_hand = EXCLUDED.quantity_on_hand`,
		itemUUID, qty); err != nil {
		t.Fatalf("give stock: %v", err)
	}
}

// seedItem creates a catalogue item with the given tracking and stock switch and
// qty on hand. A slab-tracked item needs an area unit.
func seedItem(t *testing.T, pool *pgxpool.Pool, tracking string, trackStock bool, onHand float64) string {
	t.Helper()
	unit := "EA"
	if tracking == "serialized" {
		unit = "SQFT"
	}
	var uuid string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id,
			inventory_item_unit_price, inventory_item_tracking, inventory_item_track_stock, inventory_item_created_by)
		SELECT $1, $1, u.unit_id, 10, $2, $3, 1 FROM lkp_unit u WHERE u.unit_code = $4
		RETURNING inventory_item_uuid`,
		"STK-"+uniqSuffix(), tracking, trackStock, unit).Scan(&uuid); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if onHand > 0 {
		giveStock(t, pool, uuid, onHand)
	}
	return uuid
}

func seedCustomer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var custType int
	if err := pool.QueryRow(context.Background(),
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custType); err != nil {
		t.Fatalf("resolve CUST: %v", err)
	}
	var uuid string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`, custType, "Stock Customer "+uniqSuffix()).Scan(&uuid); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return uuid
}

// line is one catalogue line of an order under test.
type line struct {
	item string
	qty  float64
}

func lines(ls ...line) []LineInput2 {
	out := make([]LineInput2, len(ls))
	for i, l := range ls {
		out[i] = LineInput2{LineNumber: i + 1, InventoryItemUUID: l.item, Quantity: l.qty}
	}
	return out
}

func orderFor(cust string, items []LineInput2) CreateOrderInput {
	return CreateOrderInput{CustomerUUID: cust, orderFields: orderFields{Items: items}}
}

// held is the open reservation the order holds on the item, less what is fulfilled.
func held(t *testing.T, pool *pgxpool.Pool, orderUUID, itemUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(a.allocated_quantity - a.fulfilled_quantity), 0)
		FROM inventory_allocation a
		JOIN sales_order so ON so.sales_order_id = a.sales_order_id
		JOIN inventory_item ii ON ii.inventory_item_id = a.inventory_item_id
		WHERE so.sales_order_uuid = $1 AND ii.inventory_item_uuid = $2
		  AND a.allocation_status IN ('reserved','partially_fulfilled')`, orderUUID, itemUUID).Scan(&v); err != nil {
		t.Fatalf("read held: %v", err)
	}
	return v
}

func onHand(t *testing.T, pool *pgxpool.Pool, itemUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(s.quantity_on_hand), 0) FROM inventory_stock s
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		WHERE ii.inventory_item_uuid = $1`, itemUUID).Scan(&v); err != nil {
		t.Fatalf("read on hand: %v", err)
	}
	return v
}

// shortageOf unwraps the stock-shortage error, failing if err is anything else.
func shortageOf(t *testing.T, err error) []inventory.Shortage {
	t.Helper()
	var se *inventory.StockShortageError
	if !errors.As(err, &se) {
		t.Fatalf("err = %v (%T), want *inventory.StockShortageError", err, err)
	}
	return se.Shortages
}

func mustCreate(t *testing.T, pool *pgxpool.Pool, cust string, items []LineInput2) *Order {
	t.Helper()
	o, err := Create(context.Background(), pool, orderFor(cust, items), 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return o
}

func orderCount(t *testing.T, pool *pgxpool.Pool, custUUID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM sales_order so JOIN customer c ON c.customer_id = so.sales_order_customer_id
		WHERE c.customer_uuid = $1`, custUUID).Scan(&n); err != nil {
		t.Fatalf("count orders: %v", err)
	}
	return n
}

// ---- the check ---------------------------------------------------------------

func TestCreate_RefusedWhenStockIsShort(t *testing.T) {
	pool := testPool(t)
	cust := seedCustomer(t, pool)
	item := seedItem(t, pool, "quantity", true, 10)

	_, err := Create(context.Background(), pool, orderFor(cust, lines(line{item, 12})), 1)

	short := shortageOf(t, err)
	if len(short) != 1 {
		t.Fatalf("shortages = %+v, want one", short)
	}
	s := short[0]
	if s.ItemID != item || s.Requested != 12 || s.Available != 10 || s.Short != 2 {
		t.Errorf("shortage = %+v, want item %s requested 12 available 10 short 2", s, item)
	}
	if s.SKU == "" || s.Name == "" || s.UnitCode != "EA" {
		t.Errorf("shortage must carry what the dialog shows: %+v", s)
	}
	// Refused means nothing was saved and nothing is held.
	if n := orderCount(t, pool, cust); n != 0 {
		t.Errorf("%d order(s) saved for a refused create, want 0", n)
	}
	if got := onHand(t, pool, item); got != 10 {
		t.Errorf("on hand = %v, want 10 untouched", got)
	}
}

func TestCreate_ReservesWhatItNeeds_AndTheNextOrderSeesIt(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)

	a := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 6}))
	if got := held(t, pool, a.ID, item); got != 6 {
		t.Fatalf("order A holds %v, want 6", got)
	}

	// Only 4 are free now: another order cannot promise the same stone.
	_, err := Create(context.Background(), pool, orderFor(seedCustomer(t, pool), lines(line{item, 5})), 1)
	short := shortageOf(t, err)
	if short[0].Requested != 5 || short[0].Available != 4 || short[0].Short != 1 {
		t.Errorf("shortage = %+v, want requested 5 available 4 short 1", short[0])
	}

	b := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 4}))
	if got := held(t, pool, b.ID, item); got != 4 {
		t.Errorf("order B holds %v, want 4", got)
	}
	// Everything is spoken for now.
	_, err = Create(context.Background(), pool, orderFor(seedCustomer(t, pool), lines(line{item, 1})), 1)
	if got := shortageOf(t, err)[0].Available; got != 0 {
		t.Errorf("available = %v, want 0", got)
	}
}

func TestCreate_AddsUpLinesForTheSameItem(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)

	// Two lines of 6 are each fine alone; together they ask for 12.
	_, err := Create(context.Background(), pool, orderFor(seedCustomer(t, pool), lines(line{item, 6}, line{item, 6})), 1)

	if got := shortageOf(t, err)[0]; got.Requested != 12 || got.Available != 10 {
		t.Errorf("shortage = %+v, want requested 12 (both lines) available 10", got)
	}
}

func TestCreate_NamesEveryShortItem(t *testing.T) {
	pool := testPool(t)
	enough := seedItem(t, pool, "quantity", true, 100)
	shortA := seedItem(t, pool, "quantity", true, 1)
	shortB := seedItem(t, pool, "serialized", true, 0)

	_, err := Create(context.Background(), pool, orderFor(seedCustomer(t, pool),
		lines(line{enough, 5}, line{shortA, 3}, line{shortB, 2})), 1)

	short := shortageOf(t, err)
	got := map[string]inventory.Shortage{}
	for _, s := range short {
		got[s.ItemID] = s
	}
	if len(short) != 2 || got[shortA].Short != 2 || got[shortB].Short != 2 {
		t.Errorf("shortages = %+v, want exactly the two short items (short 2 each)", short)
	}
	if _, ok := got[enough]; ok {
		t.Error("an item with plenty of stock must not be listed")
	}
}

func TestCreate_DoesNotCheckFreeTextOrUntrackedLines(t *testing.T) {
	pool := testPool(t)
	service := seedItem(t, pool, "quantity", false, 0) // "Installation" — nothing to run out of

	order := mustCreate(t, pool, seedCustomer(t, pool), []LineInput2{
		{LineNumber: 1, Description: "Site measure", Quantity: 1000},
		{LineNumber: 2, InventoryItemUUID: service, Quantity: 5},
	})

	if got := held(t, pool, order.ID, service); got != 0 {
		t.Errorf("an untracked item holds %v, want nothing reserved", got)
	}
	var n int
	if err := pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM inventory_allocation a JOIN sales_order so ON so.sales_order_id = a.sales_order_id WHERE so.sales_order_uuid = $1`,
		order.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d reservation row(s), want none", n)
	}
}

func TestCreate_AlwaysChecksASlabItem_EvenIfTrackingWasSwitchedOff(t *testing.T) {
	pool := testPool(t)
	slab := seedItem(t, pool, "serialized", false, 0)

	_, err := Create(context.Background(), pool, orderFor(seedCustomer(t, pool), lines(line{slab, 1})), 1)

	shortageOf(t, err)
}

// ---- editing, cancelling, deleting -------------------------------------------

func TestUpdate_RewritesTheReservation_WithoutCompetingWithItself(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)
	other := seedCustomer(t, pool)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 6}))
	update := func(qty float64) error {
		_, err := Update(context.Background(), pool, o.ID, UpdateOrderInput{orderFields: orderFields{Items: lines(line{item, qty})}}, 1)
		return err
	}

	// 9 is fine even though only 4 are free: 6 of them are this order's own.
	if err := update(9); err != nil {
		t.Fatalf("Update to 9: %v", err)
	}
	if got := held(t, pool, o.ID, item); got != 9 {
		t.Errorf("holds %v after raising to 9, want 9 (rewritten, not stacked)", got)
	}

	// 11 is more than exists: refused, and the earlier hold and lines are intact.
	shortageOf(t, update(11))
	if got := held(t, pool, o.ID, item); got != 9 {
		t.Errorf("holds %v after a refused edit, want the earlier 9", got)
	}
	back, err := Get(context.Background(), pool, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Items[0].Quantity != 9 {
		t.Errorf("line quantity = %v after a refused edit, want 9", back.Items[0].Quantity)
	}

	// Lowering it frees stock for others.
	if err := update(3); err != nil {
		t.Fatalf("Update to 3: %v", err)
	}
	mustCreate(t, pool, other, lines(line{item, 7}))
}

func TestCancel_ReleasesTheReservation(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 10}))
	if _, err := Create(context.Background(), pool, orderFor(seedCustomer(t, pool), lines(line{item, 1})), 1); err == nil {
		t.Fatal("stock is fully reserved, a second order should be refused")
	}

	if _, err := Transition(context.Background(), pool, o.ID, "CANC", 1); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	if got := held(t, pool, o.ID, item); got != 0 {
		t.Errorf("a cancelled order still holds %v", got)
	}
	mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 10}))
}

func TestSoftDelete_ReleasesTheReservation(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 10}))

	if err := SoftDelete(context.Background(), pool, o.ID, 1); err != nil {
		t.Fatalf("delete: %v", err)
	}

	mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 10}))
}

// ---- filling ------------------------------------------------------------------

// fill opens an order and fills it. The order is put straight into Open rather
// than walked through the approval checkpoint, whose behaviour depends on which
// approvers other tests left configured; only the move to Filled is under test.
func fill(t *testing.T, pool *pgxpool.Pool, orderUUID string) error {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE sales_order SET sales_order_approval_status = 'none', sales_order_status = (
			SELECT rs.record_status_id FROM lkp_record_status rs
			JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
			WHERE rt.record_type_code = 'SORD' AND rs.record_status_code = 'OPEN')
		WHERE sales_order_uuid = $1`, orderUUID); err != nil {
		t.Fatalf("open order: %v", err)
	}
	if _, err := Transition(context.Background(), pool, orderUUID, "FILL", 1); err != nil {
		return fmt.Errorf("to FILL: %w", err)
	}
	return nil
}

func TestFill_TakesQuantityItemsOutOfStock(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 4}))

	if err := fill(t, pool, o.ID); err != nil {
		t.Fatalf("fill: %v", err)
	}

	if got := onHand(t, pool, item); got != 6 {
		t.Errorf("on hand = %v after filling 4 of 10, want 6", got)
	}
	if got := held(t, pool, o.ID, item); got != 0 {
		t.Errorf("a filled order still holds %v", got)
	}
	var status string
	var fulfilled float64
	if err := pool.QueryRow(context.Background(), `
		SELECT a.allocation_status, a.fulfilled_quantity FROM inventory_allocation a
		JOIN sales_order so ON so.sales_order_id = a.sales_order_id WHERE so.sales_order_uuid = $1`, o.ID).Scan(&status, &fulfilled); err != nil {
		t.Fatal(err)
	}
	if status != "fulfilled" || fulfilled != 4 {
		t.Errorf("reservation = %s / %v fulfilled, want fulfilled / 4", status, fulfilled)
	}
	back, err := Get(context.Background(), pool, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Items[0].FulfilledQuantity != 4 || back.Items[0].Status != "filled" {
		t.Errorf("line = fulfilled %v status %q, want 4 / filled", back.Items[0].FulfilledQuantity, back.Items[0].Status)
	}
	// It was taken out once, as a ledger 'consumed' row.
	var rows int
	var delta float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*), COALESCE(SUM(l.quantity_delta),0) FROM inventory_ledger l
		JOIN inventory_item ii ON ii.inventory_item_id = l.inventory_item_id
		WHERE ii.inventory_item_uuid = $1 AND l.event = 'consumed'`, item).Scan(&rows, &delta); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || delta != -4 {
		t.Errorf("ledger consumed rows = %d totalling %v, want 1 row of -4", rows, delta)
	}
}

func TestFill_RefusedWhenTheReservedStockIsGone(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 8}))
	giveStock(t, pool, item, 3) // a count or adjustment removed stock after the order was saved

	err := fill(t, pool, o.ID)

	if err == nil || !IsClientError(errors.Unwrap(err)) {
		t.Fatalf("fill = %v, want a ClientError refusing it", err)
	}
	back, gerr := Get(context.Background(), pool, o.ID)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if back.StatusCode != "OPEN" {
		t.Errorf("status = %s after a refused fill, want OPEN", back.StatusCode)
	}
	if got := onHand(t, pool, item); got != 3 {
		t.Errorf("on hand = %v, want 3 untouched by the refused fill", got)
	}
}

func TestFill_LeavesAnOrderThatPredatesReservationsAlone(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 4}))
	// Make it look like an order saved before reservations existed.
	if _, err := pool.Exec(context.Background(), `
		DELETE FROM inventory_allocation WHERE sales_order_id = (SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1)`, o.ID); err != nil {
		t.Fatal(err)
	}

	if err := fill(t, pool, o.ID); err != nil {
		t.Fatalf("fill: %v", err)
	}

	if got := onHand(t, pool, item); got != 10 {
		t.Errorf("on hand = %v, want 10: an order with no reservation must not deduct", got)
	}
}

func TestFill_DoesNotDeductASlabItem(t *testing.T) {
	pool := testPool(t)
	slab := seedItem(t, pool, "serialized", true, 50)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{slab, 30}))

	if err := fill(t, pool, o.ID); err != nil {
		t.Fatalf("fill: %v", err)
	}

	// Its stone leaves stock when a job cuts it, not when the order is filled.
	if got := onHand(t, pool, slab); got != 50 {
		t.Errorf("on hand = %v, want 50: a slab item is deducted by cutting", got)
	}
	if got := held(t, pool, o.ID, slab); got != 0 {
		t.Errorf("the hold should still end when the order is filled, holds %v", got)
	}
}

// ---- consumption --------------------------------------------------------------

func TestApplyConsumption_ShrinksTheHold_AndSurvivesAnEdit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	slab := seedItem(t, pool, "serialized", true, 10)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{slab, 8}))
	var orderID, itemID int
	if err := pool.QueryRow(ctx, `SELECT so.sales_order_id, ii.inventory_item_id FROM sales_order so, inventory_item ii
		WHERE so.sales_order_uuid = $1 AND ii.inventory_item_uuid = $2`, o.ID, slab).Scan(&orderID, &itemID); err != nil {
		t.Fatal(err)
	}
	consume := func(area float64) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := inventory.ApplyConsumption(ctx, tx, orderID, itemID, area); err != nil {
			t.Fatalf("ApplyConsumption: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	consume(5)
	if got := held(t, pool, o.ID, slab); got != 3 {
		t.Errorf("holds %v after 5 of 8 was cut, want 3", got)
	}
	back, _ := Get(ctx, pool, o.ID)
	if back.Items[0].FulfilledQuantity != 5 || back.Items[0].Status != "partial" {
		t.Errorf("line = fulfilled %v status %q, want 5 / partial", back.Items[0].FulfilledQuantity, back.Items[0].Status)
	}

	// Editing the order must not forget the stone already cut for it.
	if _, err := Update(ctx, pool, o.ID, UpdateOrderInput{orderFields: orderFields{Items: lines(line{slab, 8})}}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := held(t, pool, o.ID, slab); got != 3 {
		t.Errorf("holds %v after an edit, want the carried-over 3", got)
	}
	back, _ = Get(ctx, pool, o.ID)
	if back.Items[0].FulfilledQuantity != 5 {
		t.Errorf("line fulfilled = %v after an edit, want 5 carried over", back.Items[0].FulfilledQuantity)
	}

	// Cutting more than was ordered stops at the order.
	consume(20)
	if got := held(t, pool, o.ID, slab); got != 0 {
		t.Errorf("holds %v after cutting past the order, want 0", got)
	}
	back, _ = Get(ctx, pool, o.ID)
	if back.Items[0].FulfilledQuantity != 8 || back.Items[0].Status != "filled" {
		t.Errorf("line = fulfilled %v status %q, want 8 / filled", back.Items[0].FulfilledQuantity, back.Items[0].Status)
	}
}

// ---- the Inventory tab ----------------------------------------------------------

func TestInventoryForOrder_ShowsWhatTheOrderHolds(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 10)
	mine := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 4}))
	mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 3}))

	rows, err := InventoryForOrder(context.Background(), pool, mine.ID)
	if err != nil {
		t.Fatalf("InventoryForOrder: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want one", rows)
	}
	r := rows[0]
	if r.OnHand != 10 || r.Allocated != 7 || r.ReservedForOrder != 4 || r.Available != 3 || r.SalesOrderQuantity != 4 || !r.Tracked {
		t.Errorf("row = %+v, want onHand 10, allocated 7, reservedForOrder 4, available 3, so qty 4, tracked", r)
	}
}

func TestInventoryForOrder_FlagsAnUntrackedItem(t *testing.T) {
	pool := testPool(t)
	service := seedItem(t, pool, "quantity", false, 0)
	o := mustCreate(t, pool, seedCustomer(t, pool), lines(line{service, 2}))

	rows, err := InventoryForOrder(context.Background(), pool, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Tracked {
		t.Errorf("rows = %+v, want one untracked row", rows)
	}
}
