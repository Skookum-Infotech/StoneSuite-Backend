// cashtransfer/store_transition.go
package cashtransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live cash transfer to toStatusCode. It is deliberately
// the *narrow* status endpoint (spec AD-6): the two moves with side effects —
// Post (creates a journal entry, moves balances) and Reverse (creates a
// reversing entry) — are refused here and routed to their own functions. What
// remains is Approve (DRFT→APPR) and Cancel (DRFT/APPR→CANC), neither of
// which touches the ledger.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*CashTransfer, error) {
	switch toStatusCode {
	case postedStatusCode:
		return nil, ClientError{Msg: "Use the post endpoint to post a cash transfer."}
	case reversedStatusCode:
		return nil, ClientError{Msg: "Use the reverse endpoint to reverse a posted cash transfer."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT ct.cash_transfer_id, ct.cash_transfer_status, rs.record_status_code
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL
		FOR UPDATE OF ct`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cash transfer for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve CTRF record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			cash_transfer_status = $2,
			cash_transfer_updated_at = NOW(), cash_transfer_updated_by = $3,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("transition cash transfer: %w", err)
	}
	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
