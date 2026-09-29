package salesorder

// stock.go — the sales order's side of stock reservation. The rules themselves
// (what is free, how a reservation is written, released and consumed) live in
// the inventory package, which the fabrication module shares; this file only
// turns an order's lines into the request and its errors into this package's.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
)

// reserveStock checks the order's catalogue lines against free stock and
// reserves them, replacing whatever the order held before. Free-text lines carry
// no item and are skipped. A shortfall comes back as *inventory.StockShortageError,
// which the controller turns into the "not enough stock" response; the caller's
// transaction is rolled back, so a refused save leaves nothing behind.
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
// so the controller's existing mapping to a 400 applies. Anything else —
// including a *StockShortageError — passes through untouched.
func asClientError(err error) error {
	var ce inventory.ClientError
	if errors.As(err, &ce) {
		return ClientError{Msg: ce.Msg}
	}
	return err
}
