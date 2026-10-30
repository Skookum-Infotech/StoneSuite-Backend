package bintransfer

// store_write.go — create, update and delete.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/workflow"
)

// ErrNotEditable is returned when a header or line write targets a transfer that
// is no longer a draft. Maps to HTTP 409.
var ErrNotEditable = errors.New("this bin transfer can no longer be edited")

// validateCustom validates custom fields against the "bin_transfer" workflow's
// field definitions, if one has been seeded. A no-op when it has not.
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if len(custom) == 0 {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "bin_transfer")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load bin_transfer workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load bin_transfer field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}

// nullableDate returns nil for a blank date so COALESCE($n::date, ...) falls back.
func nullableDate(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return strings.TrimSpace(s)
}

func writeHistory(ctx context.Context, tx workflow.Querier, transferID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO bin_transfer_history (bin_transfer_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		transferID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("record bin transfer history: %w", err)
	}
	return nil
}

// Create opens a new bin transfer in draft. Nothing moves: this is the request.
func Create(ctx context.Context, pool *pgxpool.Pool, in Input, actorEmployeeID int) (*BinTransfer, error) {
	// A retry whose first attempt already committed gets that transfer back
	// before any validation: a replay answers like the original.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Lookup(ctx, pool, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			return Get(ctx, pool, prior)
		}
	}
	if err := validateInput(&in); err != nil {
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
		return nil, fmt.Errorf("begin create bin transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Claim inside the create's transaction: a concurrent duplicate blocks
	// here until this one commits, then replays it instead of creating.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Claim(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			_ = tx.Rollback(ctx)
			return Get(ctx, pool, prior)
		}
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, err
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, StatusDraft)
	if err != nil {
		return nil, err
	}
	lines, err := resolveLines(ctx, tx, in.WarehouseID, 0, in.Lines)
	if err != nil {
		return nil, err
	}

	owner := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		owner = *in.OwnerEmployeeID
	}

	var (
		id      int
		newUUID string
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO bin_transfer (
			record_type, bin_transfer_status, warehouse_id, bin_transfer_date,
			bin_transfer_notes, bin_transfer_owner_id, bin_transfer_custom_fields, bin_transfer_created_by
		) VALUES ($1,$2,$3, COALESCE($4::date, CURRENT_DATE), $5,$6,$7,$8)
		RETURNING bin_transfer_id, bin_transfer_uuid`,
		recordTypeID, draftStatusID, in.WarehouseID, nullableDate(in.Date),
		in.Notes, nullableInt(owner), custom, nullableInt(actorEmployeeID),
	).Scan(&id, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "The chosen location does not exist."}
		}
		return nil, fmt.Errorf("insert bin transfer: %w", err)
	}
	if _, err := assignNumber(ctx, tx, int64(id)); err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" {
		if err := idempotency.Complete(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, newUUID); err != nil {
			return nil, err
		}
	}
	if err := replaceLines(ctx, tx, id, lines, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := writeHistory(ctx, tx, id, "create", nil, &draftStatusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create bin transfer: %w", err)
	}
	notifyCreated(ctx, pool, newUUID, id, actorEmployeeID)
	return Get(ctx, pool, newUUID)
}

// Update edits a draft transfer, replacing its whole line set. Only a draft can
// be edited: approval freezes the document so the moves that were signed off are
// the ones that get applied.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in Input, actorEmployeeID int) (*BinTransfer, error) {
	if err := validateInput(&in); err != nil {
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
		return nil, fmt.Errorf("begin update bin transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if !IsEditable(cur.statusCode) {
		return nil, ErrNotEditable
	}
	if err := checkLockedVersion(ctx, tx, cur.id, in.ExpectedVersion); err != nil {
		return nil, err
	}
	lines, err := resolveLines(ctx, tx, in.WarehouseID, cur.id, in.Lines)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bin_transfer SET
			warehouse_id = $2, bin_transfer_date = COALESCE($3::date, bin_transfer_date),
			bin_transfer_notes = $4, bin_transfer_owner_id = COALESCE($5, bin_transfer_owner_id),
			bin_transfer_custom_fields = $6,
			bin_transfer_updated_at = NOW(), bin_transfer_updated_by = $7,
			bin_transfer_record_version = bin_transfer_record_version + 1
		WHERE bin_transfer_id = $1`,
		cur.id, in.WarehouseID, nullableDate(in.Date), in.Notes, in.OwnerEmployeeID, custom,
		nullableInt(actorEmployeeID)); err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "The chosen location or owner does not exist."}
		}
		return nil, fmt.Errorf("update bin transfer: %w", err)
	}
	if err := replaceLines(ctx, tx, cur.id, lines, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := writeHistory(ctx, tx, cur.id, "update", &cur.statusID, &cur.statusID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update bin transfer: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// Delete soft-deletes a transfer that never moved stone: a draft, or one that
// was cancelled. A pending or approved one is cancelled first, and a completed
// one is part of the record -- the slabs really did move.
func Delete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete bin transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := lockHeader(ctx, tx, uuid)
	if err != nil {
		return err
	}
	switch cur.statusCode {
	case StatusDraft, StatusCancelled:
	case StatusCompleted:
		return ClientError{Msg: "A completed bin transfer cannot be deleted: the slabs were moved."}
	default:
		return ClientError{Msg: "Cancel this bin transfer before deleting it."}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bin_transfer SET bin_transfer_deleted_at = NOW(), bin_transfer_deleted_by = $2
		WHERE bin_transfer_id = $1`, cur.id, actorOrSystem(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete bin transfer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete bin transfer: %w", err)
	}
	return nil
}

// notifyCreated best-effort-notifies the actor that the request was raised.
func notifyCreated(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, actorEmployeeID int) {
	cfg := moduleConfig()
	approvalchain.NotifyCreated(ctx, pool, approvalchain.EventContext{
		Table: cfg.Record.Table, IDColumn: cfg.Record.IDColumn, NumberColumn: cfg.Record.NumberColumn,
		InternalID: internalID, ActorEmployeeID: actorEmployeeID,
		Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid,
	})
}
