package creditmemo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SoftDelete marks a credit memo deleted (paired deleted_at/deleted_by).
// Blocked while any live credit_memo_application references it — unapply (or
// void, which cascades) first, so every visible ledger row's parent memo stays
// resolvable (spec AD-16).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	internalID, _, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete credit memo: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock order is credit_memo < payment. A memo issued from a payment gives its
	// money back to that payment when it goes, so the payment is locked and its
	// rollup recomputed in the same transaction.
	var sourcePaymentID *int
	if err := tx.QueryRow(ctx, `
		SELECT credit_memo_source_payment_id FROM credit_memo
		WHERE credit_memo_id = $1 AND credit_memo_deleted_at IS NULL FOR UPDATE`, internalID,
	).Scan(&sourcePaymentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock credit memo for delete: %w", err)
	}
	var liveApplications int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM credit_memo_application WHERE credit_memo_id = $1 AND application_deleted_at IS NULL`,
		internalID).Scan(&liveApplications); err != nil {
		return fmt.Errorf("count live credit applications: %w", err)
	}
	if liveApplications > 0 {
		return ClientError{Msg: "Cannot delete a credit memo with live applications; unapply them first."}
	}
	if sourcePaymentID != nil {
		if _, err := lockLinkedPayment(ctx, tx, *sourcePaymentID); err != nil {
			return err
		}
	}

	before, err := getQ(ctx, tx, id)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE credit_memo
		SET credit_memo_deleted_at = NOW(), credit_memo_deleted_by = $1
		WHERE credit_memo_id = $2 AND credit_memo_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), internalID)
	if err != nil {
		return fmt.Errorf("delete credit memo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if sourcePaymentID != nil {
		if err := recomputePaymentCredited(ctx, tx, *sourcePaymentID, actorEmployeeID); err != nil {
			return err
		}
	}
	if err := auditTx(ctx, tx, auditDelete, id, before, employeeExtra(actorEmployeeID)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete credit memo: %w", err)
	}
	return nil
}
