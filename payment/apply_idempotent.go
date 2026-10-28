package payment

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/idempotency"
)

// ApplyIdempotent is Apply made replay-safe by the idempotency ledger: the key
// is claimed inside the apply transaction, so a retried request returns the
// payment as the first attempt left it instead of adding the amount again.
// requestHash must fingerprint the whole request (payment, invoice, amount).
// An empty key behaves exactly like Apply.
func ApplyIdempotent(ctx context.Context, pool *pgxpool.Pool, paymentUUID, invoiceUUID string, amount float64, actorEmployeeID int, key, requestHash string) (*Payment, error) {
	if key == "" {
		return Apply(ctx, pool, paymentUUID, invoiceUUID, amount, actorEmployeeID)
	}
	if err := validateAmount("amount", amount); err != nil {
		return nil, err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	prior, err := idempotency.Claim(ctx, tx, idempotencyScopeApply, key, requestHash)
	if err != nil {
		return nil, err
	}
	if prior != "" {
		_ = tx.Rollback(ctx)
		return Get(ctx, pool, prior)
	}
	if err := applyTx(ctx, tx, paymentUUID, invoiceUUID, amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := idempotency.Complete(ctx, tx, idempotencyScopeApply, key, paymentUUID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit apply: %w", err)
	}
	return Get(ctx, pool, paymentUUID)
}
