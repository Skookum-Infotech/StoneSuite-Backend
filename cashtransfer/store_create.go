package cashtransfer

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Create inserts a new Draft cash transfer inside one transaction: validates
// the two accounts (different, active, postable, Bank/Cash — spec AD-7),
// validates custom fields, inserts the header, assigns the document number,
// and writes the 'create' history row.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateInput, actorEmployeeID int) (*CashTransfer, error) {
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

	typeID, err := recordTypeIDByCode(ctx, pool, recordTypeCode)
	if err != nil {
		return nil, err
	}
	draftStatusID, err := statusIDByCode(ctx, pool, typeID, draftStatusCode)
	if err != nil {
		return nil, err
	}

	ownerEmp := in.OwnerEmployeeID
	if ownerEmp == nil && actorEmployeeID != 0 {
		ownerEmp = &actorEmployeeID
	}

	transferDate := time.Now()
	if in.TransferDate != nil {
		transferDate = *in.TransferDate
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create cash transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO cash_transfer (
			record_type, cash_transfer_status, cash_transfer_date,
			from_account_id, to_account_id, cash_transfer_amount, cash_transfer_reference,
			cash_transfer_notes, cash_transfer_internal_notes,
			cash_transfer_owner_id, cash_transfer_custom_fields,
			cash_transfer_created_by, cash_transfer_updated_by
		) VALUES (
			$1,$2,$3, $4,$5,$6,$7, $8,$9, $10,$11, $12,$12
		) RETURNING cash_transfer_id, cash_transfer_uuid`,
		typeID, draftStatusID, transferDate,
		fromID, toID, in.Amount, in.Reference,
		in.Notes, in.InternalNotes,
		ownerEmp, custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert cash transfer: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE cash_transfer SET cash_transfer_number = $1 WHERE cash_transfer_id = $2`,
		number, newID); err != nil {
		return nil, fmt.Errorf("set cash transfer number: %w", err)
	}

	writeHistory(ctx, tx, newID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create cash transfer: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
