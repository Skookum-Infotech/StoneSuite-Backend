package cashtransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update edits a cash transfer's fields. Only a Draft transfer may be edited
// (spec: "prevent edits after posting") — every other status returns a
// ClientError (400).
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateInput, actorEmployeeID int) (*CashTransfer, error) {
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	if in.FromAccountUUID == "" || in.ToAccountUUID == "" {
		return nil, ClientError{Msg: "fromAccountUuid and toAccountUuid are required."}
	}
	if in.FromAccountUUID == in.ToAccountUUID {
		return nil, ClientError{Msg: "source and destination accounts must be different."}
	}
	fromID, err := resolveAccount(ctx, pool, in.FromAccountUUID, "Source")
	if err != nil {
		return nil, err
	}
	toID, err := resolveAccount(ctx, pool, in.ToAccountUUID, "Destination")
	if err != nil {
		return nil, err
	}
	if fromID == toID {
		return nil, ClientError{Msg: "source and destination accounts must be different."}
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
		return nil, fmt.Errorf("begin update cash transfer: %w", err)
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
		return nil, fmt.Errorf("load cash transfer for update: %w", err)
	}
	if curStatusCode != draftStatusCode {
		return nil, ClientError{Msg: "Only a draft cash transfer can be edited."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			from_account_id = $2, to_account_id = $3, cash_transfer_amount = $4,
			cash_transfer_date = COALESCE($5, cash_transfer_date),
			cash_transfer_reference = $6, cash_transfer_notes = $7, cash_transfer_internal_notes = $8,
			cash_transfer_owner_id = COALESCE($9, cash_transfer_owner_id),
			cash_transfer_custom_fields = $10,
			cash_transfer_updated_at = NOW(), cash_transfer_updated_by = $11,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`,
		internalID, fromID, toID, in.Amount, in.TransferDate,
		in.Reference, in.Notes, in.InternalNotes, in.OwnerEmployeeID, custom, nullableInt(actorEmployeeID),
	); err != nil {
		return nil, fmt.Errorf("update cash transfer: %w", err)
	}
	writeHistory(ctx, tx, internalID, "update", &curStatusID, &curStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update cash transfer: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// SoftDelete removes a Draft cash transfer (spec AD-9: CRUD parity; Draft
// only, same guard as Update).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete cash transfer: %w", err)
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
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load cash transfer for delete: %w", err)
	}
	if curStatusCode != draftStatusCode {
		return ClientError{Msg: "Only a draft cash transfer can be deleted."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			cash_transfer_deleted_at = NOW(), cash_transfer_deleted_by = $2,
			cash_transfer_updated_at = NOW(), cash_transfer_updated_by = $2,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`, internalID, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete cash transfer: %w", err)
	}
	writeHistory(ctx, tx, internalID, "delete", &curStatusID, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete cash transfer: %w", err)
	}
	return nil
}
