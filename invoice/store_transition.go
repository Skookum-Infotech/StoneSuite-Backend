package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves an invoice to toStatusCode after validating the move against
// the static transition map. The invoice row is locked for the rest of the
// transaction so concurrent transitions serialize.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*Invoice, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT i.invoice_id, i.invoice_status, i.record_type, rs.record_status_code
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL
		FOR UPDATE OF i`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve invoice for transition: %w", err)
	}

	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE invoice SET invoice_status = $1, invoice_updated_at = NOW(),
			invoice_updated_by = $2, invoice_record_version = invoice_record_version + 1
		WHERE invoice_id = $3`, toStatusID, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update invoice status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert invoice transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}
