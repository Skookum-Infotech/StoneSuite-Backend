package salesorder

// stock.go — the sales order's side of stock reservation. The rules themselves
// (what is free, how a reservation is written, released and consumed) live in
// the inventory package, which the fabrication module shares; this file only
// turns an order's lines into the request and its errors into this package's.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
)

// reserveStock checks the order's catalogue lines against free stock and
// reserves them, replacing whatever the order held before. Free-text lines carry
// no item and are skipped. A shortfall does not refuse the save: the full
// quantity is reserved as a backorder and StockShortages reports the gap.
//
// lineIDs are the ids of the just-inserted lines, in the same order as lines.
func reserveStock(ctx context.Context, tx pgx.Tx, orderID int, lines []resolvedLine, lineIDs []int, actorEmployeeID int) error {
	reserve := make([]inventory.ReserveLine, 0, len(lines))
	for i, l := range lines {
		if l.inventoryItemID == nil {
			continue
		}
		reserve = append(reserve, inventory.ReserveLine{
			LineID: lineIDs[i], ItemID: *l.inventoryItemID, WarehouseID: l.warehouseID, Quantity: l.quantity,
		})
	}
	return asClientError(inventory.ReserveOrder(ctx, tx, orderID, reserve, actorEmployeeID))
}

// asClientError re-wraps the inventory package's ClientError as this package's,
// so the controller's existing mapping to a 400 applies. Anything else passes
// through untouched.
func asClientError(err error) error {
	var ce inventory.ClientError
	if errors.As(err, &ce) {
		return ClientError{Msg: ce.Msg}
	}
	return err
}

// StockShortages is what the live order holds that stock cannot cover yet (nil
// when all of it is covered). See inventory.OrderShortages.
func StockShortages(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]inventory.Shortage, error) {
	var id int
	err := pool.QueryRow(ctx,
		`SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1 AND sales_order_deleted_at IS NULL`, uuid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve sales order for shortages: %w", err)
	}
	return inventory.OrderShortages(ctx, pool, id)
}
