// itemreceipt/store_transition.go
package itemreceipt

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live item receipt to toStatusCode.
//
// It is deliberately the *narrow* status endpoint: the two moves that carry
// side effects — posting (PEND → PART/RCVD, which touches stock) and voiding
// (→ VOID, which gives it back) — are refused here and routed to Post and Void,
// which do the work transactionally. What remains is nothing, today; the map
// has no side-effect-free edges. The handler exists so the module's status
// surface is complete and so a future status (an inspection hold, say) does not
// need a new endpoint.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*ItemReceipt, error) {
	switch toStatusCode {
	case partialStatusCode, receivedStatusCode:
		return nil, ClientError{Msg: "Use the post endpoint to receive goods against the order."}
	case voidStatusCode:
		return nil, ClientError{Msg: "Use the void endpoint to reverse a receipt."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT ir.item_receipt_id, ir.item_receipt_status, rs.record_status_code
		FROM item_receipt ir
		JOIN lkp_record_status rs ON rs.record_status_id = ir.item_receipt_status
		WHERE ir.item_receipt_uuid = $1 AND ir.item_receipt_deleted_at IS NULL
		FOR UPDATE OF ir`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load item receipt for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, irctRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve IRCT record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE item_receipt SET
			item_receipt_status = $2,
			item_receipt_updated_at = NOW(),
			item_receipt_updated_by = $3,
			item_receipt_record_version = item_receipt_record_version + 1
		WHERE item_receipt_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("transition item receipt: %w", err)
	}
	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
