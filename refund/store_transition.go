package refund

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a refund to toStatusCode after validating the move
// against the static transition map. Moving to VOID first reverses every
// live application on this refund (spec AD-3/AD-9) — reversed
// credit-memo-sources before payment-sources, extending the existing lock
// order refund < credit_memo < payment < invoice (AD-7).
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*Refund, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT rfnd.refund_id, rfnd.refund_status, rfnd.record_type, rs.record_status_code
		FROM refund rfnd
		JOIN lkp_record_status rs ON rs.record_status_id = rfnd.refund_status
		WHERE rfnd.refund_uuid = $1 AND rfnd.refund_deleted_at IS NULL
		FOR UPDATE OF rfnd`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve refund for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}
	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	if toStatusCode == "VOID" {
		type liveApp struct {
			paymentID    *int
			creditMemoID *int
		}
		// ORDER BY (credit_memo_id IS NULL) puts credit-memo-sourced rows first,
		// then payment-sourced rows, matching the credit_memo < payment lock
		// order (AD-7); COALESCE ids within each group fixes a total order so two
		// concurrent VOID cascades touching the same sources can't deadlock.
		rows, err := tx.Query(ctx, `
			SELECT payment_id, credit_memo_id FROM refund_application
			WHERE refund_id = $1 AND application_deleted_at IS NULL
			ORDER BY (credit_memo_id IS NULL), COALESCE(credit_memo_id, 0), COALESCE(payment_id, 0)`, internalID)
		if err != nil {
			return nil, fmt.Errorf("list live applications: %w", err)
		}
		var apps []liveApp
		for rows.Next() {
			var a liveApp
			if err := rows.Scan(&a.paymentID, &a.creditMemoID); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan application source: %w", err)
			}
			apps = append(apps, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list live applications: %w", err)
		}

		var touchedPayment, touchedCreditMemo bool
		for _, a := range apps {
			if a.creditMemoID != nil {
				if _, err := tx.Exec(ctx, `SELECT credit_memo_id FROM credit_memo WHERE credit_memo_id = $1 FOR UPDATE`, *a.creditMemoID); err != nil {
					return nil, fmt.Errorf("lock credit memo source: %w", err)
				}
				if _, err := tx.Exec(ctx, `
					UPDATE refund_application SET application_deleted_at = NOW(), application_deleted_by = $1
					WHERE refund_id = $2 AND credit_memo_id = $3 AND application_deleted_at IS NULL`,
					actorOrSystem(actorEmployeeID), internalID, *a.creditMemoID); err != nil {
					return nil, fmt.Errorf("cascade-unapply credit memo: %w", err)
				}
				if err := recomputeCreditMemoRefunded(ctx, tx, *a.creditMemoID, actorEmployeeID); err != nil {
					return nil, err
				}
				touchedCreditMemo = true
			} else if a.paymentID != nil {
				if _, err := tx.Exec(ctx, `SELECT payment_id FROM payment WHERE payment_id = $1 FOR UPDATE`, *a.paymentID); err != nil {
					return nil, fmt.Errorf("lock payment source: %w", err)
				}
				if _, err := tx.Exec(ctx, `
					UPDATE refund_application SET application_deleted_at = NOW(), application_deleted_by = $1
					WHERE refund_id = $2 AND payment_id = $3 AND application_deleted_at IS NULL`,
					actorOrSystem(actorEmployeeID), internalID, *a.paymentID); err != nil {
					return nil, fmt.Errorf("cascade-unapply payment: %w", err)
				}
				if err := recomputePaymentRefunded(ctx, tx, *a.paymentID, actorEmployeeID); err != nil {
					return nil, err
				}
				touchedPayment = true
			}
		}
		if touchedPayment || touchedCreditMemo {
			var amt float64
			if err := tx.QueryRow(ctx, `SELECT refund_amount FROM refund WHERE refund_id = $1`, internalID).Scan(&amt); err != nil {
				return nil, fmt.Errorf("reload refund amount: %w", err)
			}
			if err := recomputeRefund(ctx, tx, internalID, amt, actorEmployeeID); err != nil {
				return nil, err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE refund SET refund_status = $1, refund_updated_at = NOW(),
			refund_updated_by = $2, refund_record_version = refund_record_version + 1
		WHERE refund_id = $3`, toStatusID, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update refund status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO refund_history (refund_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert refund transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}
