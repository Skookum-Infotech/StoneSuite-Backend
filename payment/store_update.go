package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

func internalIDByUUID(ctx context.Context, pool *pgxpool.Pool, id string) (int, error) {
	var internalID int
	err := pool.QueryRow(ctx,
		`SELECT payment_id FROM payment WHERE payment_uuid = $1 AND payment_deleted_at IS NULL`, id).Scan(&internalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve payment: %w", err)
	}
	return internalID, nil
}

// Update edits non-monetary fields only (spec AD-10: amount is immutable
// post-creation — void + recreate to correct it).
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in UpdatePaymentInput, actorEmployeeID int) (*Payment, error) {
	internalID, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if err := resolveMethod(ctx, pool, in.MethodID); err != nil {
		return nil, err
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		UPDATE payment SET
			payment_method = $1, payment_reference_number = $2, payment_date = COALESCE($3, payment_date),
			payment_currency = $4, payment_owner_id = COALESCE($5, payment_owner_id),
			payment_memo = $6, payment_internal_notes = $7, payment_custom_fields = $8,
			payment_updated_at = NOW(), payment_updated_by = $9, payment_record_version = payment_record_version + 1
		WHERE payment_id = $10`,
		in.MethodID, in.ReferenceNumber, in.PaymentDate, in.CurrencyID, in.OwnerEmployeeID,
		in.Memo, in.InternalNotes, custom, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update payment: %w", err)
	}
	// Saving an edit is how a rejected payment goes back to its approvers.
	resubmitted, err := approvalchain.ResubmitAfterEdit(ctx, tx, moduleConfig(), internalID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update payment: %w", err)
	}
	resubmitted.Notify(ctx, pool, id, actorEmployeeID)
	return Get(ctx, pool, id)
}

// SoftDelete marks a payment deleted (paired deleted_at/deleted_by). Blocked
// (409-mapped ClientError) while any live application references it — must
// Unapply (or Transition to VOID, which cascades) first (spec AD-11).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	internalID, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Locked so a credit memo cannot be issued from the payment between the
	// check below and the delete.
	var credited float64
	if err := tx.QueryRow(ctx,
		`SELECT payment_credited_total FROM payment WHERE payment_id = $1 FOR UPDATE`,
		internalID).Scan(&credited); err != nil {
		return fmt.Errorf("lock payment for delete: %w", err)
	}
	if credited > 0 {
		return ClientError{Msg: "Cannot delete a payment that has credit memos issued from it; void or delete those credit memos first."}
	}
	var liveApplications int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM payment_application WHERE payment_id = $1 AND application_deleted_at IS NULL`,
		internalID).Scan(&liveApplications); err != nil {
		return fmt.Errorf("count live applications: %w", err)
	}
	if liveApplications > 0 {
		return ClientError{Msg: "Cannot delete a payment with live applications; unapply or void it first."}
	}
	deletedBy := actorOrSystem(actorEmployeeID)
	tag, err := tx.Exec(ctx, `
		UPDATE payment SET payment_deleted_at = NOW(), payment_deleted_by = $1
		WHERE payment_uuid = $2 AND payment_deleted_at IS NULL`, deletedBy, id)
	if err != nil {
		return fmt.Errorf("delete payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete payment: %w", err)
	}
	return nil
}
