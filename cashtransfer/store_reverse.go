// cashtransfer/store_reverse.go — the reversal of Post.
package cashtransfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/journal"
)

// Reverse creates a reversing journal entry for a Posted transfer, restoring
// both accounts' balances to their pre-post values, and moves the transfer to
// Reversed. Only valid from Posted (spec: "Reverse only valid for
// already-posted transfers").
func Reverse(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*CashTransfer, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reverse cash transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ctInternalID, curStatusID int
	var curStatusCode string
	var journalEntryID *int
	err = tx.QueryRow(ctx, `
		SELECT ct.cash_transfer_id, ct.cash_transfer_status, rs.record_status_code, ct.journal_entry_id
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL
		FOR UPDATE OF ct`, uuid,
	).Scan(&ctInternalID, &curStatusID, &curStatusCode, &journalEntryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cash transfer for reversal: %w", err)
	}
	if curStatusCode != postedStatusCode {
		return nil, ErrNotPosted
	}
	if journalEntryID == nil {
		return nil, fmt.Errorf("posted cash transfer %s has no journal_entry_id (data invariant violated)", uuid)
	}

	reversalDate := time.Now()
	closed, err := journal.IsPeriodClosed(ctx, tx, reversalDate)
	if err != nil {
		return nil, fmt.Errorf("check accounting period: %w", err)
	}
	if closed {
		return nil, ErrPeriodClosed
	}

	reversingEntry, err := journal.ReverseEntry(ctx, tx, *journalEntryID, reversalDate,
		fmt.Sprintf("Reversal of cash transfer %s", uuid), actorEmployeeID)
	if err != nil {
		return nil, fmt.Errorf("create reversing journal entry: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve CTRF record type: %w", err)
	}
	reversedStatusID, err := statusIDByCode(ctx, tx, recordTypeID, reversedStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve RVSD status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			cash_transfer_status = $2,
			reversal_journal_entry_id = $3,
			cash_transfer_reversed_at = NOW(),
			cash_transfer_reversed_by = $4,
			cash_transfer_updated_at = NOW(),
			cash_transfer_updated_by = $4,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`,
		ctInternalID, reversedStatusID, reversingEntry.InternalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("reverse cash transfer: %w", err)
	}
	writeHistory(ctx, tx, ctInternalID, "reverse", &curStatusID, &reversedStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reverse cash transfer: %w", err)
	}
	return Get(ctx, pool, uuid)
}
