// cashtransfer/store_post.go — the act that makes a transfer real.
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

// Post advances an Approved transfer to Posted: it locks the header row,
// verifies it hasn't already been posted, checks the accounting period isn't
// closed, builds a balanced two-line journal entry (debit the destination
// account, credit the source account), and updates both accounts' running
// balances — all inside one transaction, so a concurrent second Post call
// blocks on the row lock and then observes the already-posted status instead
// of double-posting (mirrors itemreceipt.Post).
func Post(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*CashTransfer, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin post cash transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ctInternalID, curStatusID, fromAccountID, toAccountID int
	var curStatusCode string
	var amount float64
	var transferDate time.Time
	err = tx.QueryRow(ctx, `
		SELECT ct.cash_transfer_id, ct.cash_transfer_status, rs.record_status_code,
		       ct.from_account_id, ct.to_account_id, ct.cash_transfer_amount, ct.cash_transfer_date
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL
		FOR UPDATE OF ct`, uuid,
	).Scan(&ctInternalID, &curStatusID, &curStatusCode, &fromAccountID, &toAccountID, &amount, &transferDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cash transfer for posting: %w", err)
	}
	if IsPosted(curStatusCode) {
		return nil, ErrAlreadyPosted
	}
	if err := ValidateTransition(curStatusCode, postedStatusCode); err != nil {
		return nil, err
	}

	// journal.CreateEntry applies this same guard, so this call is not what
	// makes posting safe — it is what makes the failure legible, rejecting the
	// transfer before any account eligibility work runs and with a
	// cash-transfer-shaped error rather than a raw journal one.
	if err := journal.CheckPeriodOpen(ctx, tx, transferDate); err != nil {
		return nil, translatePeriodError(err)
	}

	if err := checkAccountEligible(ctx, tx, fromAccountID, "Source"); err != nil {
		return nil, err
	}
	if err := checkAccountEligible(ctx, tx, toAccountID, "Destination"); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve CTRF record type: %w", err)
	}
	postedStatusID, err := statusIDByCode(ctx, tx, recordTypeID, postedStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve POST status: %w", err)
	}

	entry, err := journal.CreateEntry(ctx, tx, journal.CreateEntryInput{
		EntryDate:  transferDate,
		Memo:       fmt.Sprintf("Cash transfer %s", uuid),
		SourceType: sourceType,
		SourceID:   uuid,
		CreatedBy:  actorEmployeeID,
		Lines: []journal.LineInput{
			{AccountID: toAccountID, Debit: amount},
			{AccountID: fromAccountID, Credit: amount},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create journal entry: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			cash_transfer_status = $2,
			journal_entry_id = $3,
			cash_transfer_posted_at = NOW(),
			cash_transfer_posted_by = $4,
			cash_transfer_updated_at = NOW(),
			cash_transfer_updated_by = $4,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`,
		ctInternalID, postedStatusID, entry.InternalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("post cash transfer: %w", err)
	}
	writeHistory(ctx, tx, ctInternalID, "post", &curStatusID, &postedStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit post cash transfer: %w", err)
	}
	return Get(ctx, pool, uuid)
}
