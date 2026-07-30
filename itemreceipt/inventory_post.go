package itemreceipt

// inventory_post.go — the only place this module touches stock.
//
// The implementation now lives in inventory.LedgerAndStock, which is shared
// with every other module that moves non-serialized stock. This file is the
// adapter that keeps itemreceipt's own error surface unchanged: callers here
// match on itemreceipt.ErrMovementAlreadyApplied and itemreceipt.ClientError,
// and store_post.go / store_void.go map those to HTTP status codes.

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
)

// ledgerAndStock appends one inventory_ledger row and applies the same signed
// delta to inventory_stock, translating the shared package's errors into this
// module's.
func ledgerAndStock(
	ctx context.Context, tx pgx.Tx,
	itemID, warehouseID int, event string, delta float64,
	sourceRecordTypeID, sourceRecordID, sourceLineID, actorEmployeeID int,
) error {
	err := inventory.LedgerAndStock(ctx, tx, itemID, warehouseID, event, delta,
		sourceRecordTypeID, sourceRecordID, sourceLineID, actorEmployeeID)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, inventory.ErrMovementAlreadyApplied):
		return ErrMovementAlreadyApplied
	case inventory.IsClientError(err):
		// Re-wrap so this module's callers keep matching on their own type.
		return ClientError{Msg: err.Error()}
	}
	return err
}
