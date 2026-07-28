package cashtransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

const headerSelect = `
	SELECT ct.cash_transfer_uuid, COALESCE(ct.cash_transfer_number,''),
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       ct.cash_transfer_date,
	       fa.coa_account_uuid, fa.coa_account_code, fa.coa_account_name,
	       ta.coa_account_uuid, ta.coa_account_code, ta.coa_account_name,
	       ct.cash_transfer_amount, ct.cash_transfer_reference,
	       ct.cash_transfer_notes, ct.cash_transfer_internal_notes,
	       COALESCE(ou.id::text,''), ct.cash_transfer_owner_id,
	       ct.cash_transfer_custom_fields,
	       je.journal_entry_uuid, rje.journal_entry_uuid,
	       ct.cash_transfer_posted_at, ct.cash_transfer_reversed_at,
	       ct.cash_transfer_created_at, ct.cash_transfer_updated_at, ct.cash_transfer_record_version,
	       ct.cash_transfer_id, ct.cash_transfer_status, ct.from_account_id, ct.to_account_id
	FROM cash_transfer ct
	JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
	JOIN coa_account fa ON fa.coa_account_id = ct.from_account_id
	JOIN coa_account ta ON ta.coa_account_id = ct.to_account_id
	LEFT JOIN employee oe ON oe.employee_id = ct.cash_transfer_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id
	LEFT JOIN journal_entry je ON je.journal_entry_id = ct.journal_entry_id
	LEFT JOIN journal_entry rje ON rje.journal_entry_id = ct.reversal_journal_entry_id`

type ctMeta struct {
	internalID    int
	statusID      int
	fromAccountID int
	toAccountID   int
}

func scanCT(row pgx.Row) (*CashTransfer, ctMeta, error) {
	var (
		ct         CashTransfer
		ownerEmpID *int
		custom     map[string]any
		jeUUID     *string
		rjeUUID    *string
		meta       ctMeta
	)
	err := row.Scan(
		&ct.ID, &ct.Number,
		&ct.StatusCode, &ct.StatusName,
		&ct.TransferDate,
		&ct.FromAccount.ID, &ct.FromAccount.Code, &ct.FromAccount.Name,
		&ct.ToAccount.ID, &ct.ToAccount.Code, &ct.ToAccount.Name,
		&ct.Amount, &ct.Reference,
		&ct.Notes, &ct.InternalNotes,
		&ct.OwnerUserID, &ownerEmpID,
		&custom,
		&jeUUID, &rjeUUID,
		&ct.PostedAt, &ct.ReversedAt,
		&ct.CreatedAt, &ct.UpdatedAt, &ct.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.fromAccountID, &meta.toAccountID,
	)
	if err != nil {
		return nil, ctMeta{}, err
	}
	ct.OwnerEmployeeID = ownerEmpID
	if custom == nil {
		custom = map[string]any{}
	}
	ct.CustomFields = custom
	ct.JournalEntryID = jeUUID
	ct.ReversalJournalEntryID = rjeUUID
	return &ct, meta, nil
}

// Get loads a single live cash transfer by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*CashTransfer, error) {
	ct, _, err := scanCT(pool.QueryRow(ctx, headerSelect+`
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get cash transfer: %w", err)
	}
	return ct, nil
}

func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve record type %s: %w", code, err)
	}
	return id, nil
}

func statusIDByCode(ctx context.Context, q workflow.Querier, typeID int, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx,
		`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve status %s: %w", code, err)
	}
	return id, nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// writeHistory records one row to cash_transfer_history. Best-effort, like
// itemreceipt.writeHistory — a history-write failure must never roll back the
// primary operation it is documenting.
func writeHistory(ctx context.Context, tx pgx.Tx, ctInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO cash_transfer_history (cash_transfer_id, from_status_id, to_status_id, history_action, history_by)
		VALUES ($1,$2,$3,$4,$5)`,
		ctInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// validateCustom validates in.CustomFields against the "cash_transfer"
// workflow's field definitions, if one has been seeded. No-ops when it
// hasn't (mirrors payment.validateCustom).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "cash_transfer")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load cash_transfer workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load cash_transfer field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}

// resolveAccount validates that accountUUID names a live, active, postable
// Bank or Cash account (spec AD-7) and returns its internal id. label is
// "Source" or "Destination", used in the error message.
func resolveAccount(ctx context.Context, pool *pgxpool.Pool, accountUUID, label string) (int, error) {
	var id int
	var acctType string
	var active, postable bool
	err := pool.QueryRow(ctx, `
		SELECT coa_account_id, coa_account_type, coa_account_is_active, coa_account_is_postable
		FROM coa_account
		WHERE coa_account_uuid = $1 AND coa_account_deleted_at IS NULL`, accountUUID,
	).Scan(&id, &acctType, &active, &postable)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ClientError{Msg: fmt.Sprintf("Unknown %s account.", label)}
	}
	if err != nil {
		return 0, fmt.Errorf("resolve %s account: %w", label, err)
	}
	if acctType != "bank" && acctType != "cash" {
		return 0, ClientError{Msg: fmt.Sprintf("%s account must be a Bank or Cash account.", label)}
	}
	if !active || !postable {
		return 0, ClientError{Msg: fmt.Sprintf("%s account is not active and postable.", label)}
	}
	return id, nil
}
