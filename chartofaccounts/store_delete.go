package chartofaccounts

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SoftDelete retires a user-created account. Seeded (is_system) accounts can
// never be deleted -- chk_coa_system_undeletable enforces that at the database
// level too, but returning a 409 explains why.
//
// Blocked by: a default slot pointing at the account (AD-7), or any live child.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, employeeID int) error {
	if !validAccountUUID(uuid) {
		return ClientError{Msg: fmt.Sprintf("%q is not a valid account id.", uuid)}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete account: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	cur, err := loadCurrent(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if cur.isSystem {
		return ConflictError{Msg: fmt.Sprintf(
			"Account %s %s is a standard account and cannot be deleted. Deactivate it instead.",
			cur.code, cur.name)}
	}
	if err := guardRetire(ctx, tx, cur.id, cur.code, cur.name); err != nil {
		return err
	}
	kids, err := hasLiveChildren(ctx, tx, cur.id)
	if err != nil {
		return err
	}
	if kids {
		return ConflictError{Msg: fmt.Sprintf(
			"Account %s %s still has sub-accounts. Delete them first.", cur.code, cur.name)}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE coa_account
		SET coa_account_deleted_at = CURRENT_TIMESTAMP,
		    coa_account_deleted_by = $2,
		    coa_account_is_active = FALSE, coa_account_is_visible = FALSE,
		    coa_account_record_version = coa_account_record_version + 1
		WHERE coa_account_id = $1 AND coa_account_deleted_at IS NULL`,
		cur.id, nullableInt(employeeID)); err != nil {
		return fmt.Errorf("soft delete account: %w", err)
	}

	if err := appendHistory(ctx, tx, historyRow{
		AccountID: &cur.id, Action: actionDelete, Field: "code",
		OldValue: cur.code, EmployeeID: employeeID,
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete account: %w", err)
	}
	return nil
}
