package refund

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func internalIDByUUID(ctx context.Context, pool *pgxpool.Pool, id string) (int, error) {
	var internalID int
	err := pool.QueryRow(ctx,
		`SELECT refund_id FROM refund WHERE refund_uuid = $1 AND refund_deleted_at IS NULL`, id).Scan(&internalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve refund: %w", err)
	}
	return internalID, nil
}

// Update edits non-monetary fields only (spec AD-8: amount and source
// identity are immutable post-creation — void + recreate to correct them).
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in UpdateRefundInput, actorEmployeeID int) (*Refund, error) {
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
	_, err = pool.Exec(ctx, `
		UPDATE refund SET
			refund_method = $1, refund_reference_number = $2, refund_date = COALESCE($3, refund_date),
			refund_currency = $4, refund_owner_id = COALESCE($5, refund_owner_id),
			refund_reason = $6, refund_memo = $7, refund_internal_notes = $8, refund_custom_fields = $9,
			refund_updated_at = NOW(), refund_updated_by = $10, refund_record_version = refund_record_version + 1
		WHERE refund_id = $11`,
		in.MethodID, in.ReferenceNumber, in.RefundDate, in.CurrencyID, in.OwnerEmployeeID,
		in.Reason, in.Memo, in.InternalNotes, custom, nullableInt(actorEmployeeID), internalID)
	if err != nil {
		return nil, fmt.Errorf("update refund: %w", err)
	}
	return Get(ctx, pool, id)
}

// SoftDelete marks a refund deleted (paired deleted_at/deleted_by). Blocked
// (409-mapped ClientError) while any live application references it — must
// Unapply (or Transition to VOID, which cascades) first (spec AD-9).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	internalID, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return err
	}
	var liveApplications int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM refund_application WHERE refund_id = $1 AND application_deleted_at IS NULL`,
		internalID).Scan(&liveApplications); err != nil {
		return fmt.Errorf("count live applications: %w", err)
	}
	if liveApplications > 0 {
		return ClientError{Msg: "Cannot delete a refund with live applications; unapply or void it first."}
	}
	deletedBy := actorOrSystem(actorEmployeeID)
	tag, err := pool.Exec(ctx, `
		UPDATE refund SET refund_deleted_at = NOW(), refund_deleted_by = $1
		WHERE refund_uuid = $2 AND refund_deleted_at IS NULL`, deletedBy, id)
	if err != nil {
		return fmt.Errorf("delete refund: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
