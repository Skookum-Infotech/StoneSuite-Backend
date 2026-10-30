// Package idempotency makes create endpoints safe to retry. A client sends an
// Idempotency-Key header; the handler claims (scope, key) inside the same
// transaction that creates the document and records the new document's id
// before commit. A retry with the same key and body finds that row and gets
// the original document back instead of a duplicate. With no key, nothing
// changes. Rows live in the tenant table request_idempotency.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrKeyReuse means the key was already used for a different request body.
var ErrKeyReuse = errors.New("idempotency key was already used with a different request")

// ErrInProgress means the key is claimed but no result was recorded with it.
// Claim and Complete share one transaction, so this is only reachable if a
// caller claims without completing; it is refused rather than replayed empty.
var ErrInProgress = errors.New("a request with this idempotency key has no recorded result")

// Querier is the subset of pgx.Tx the ledger needs; pass the transaction that
// creates the document, never the pool.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// HashRequest fingerprints a request body so a reused key with a different
// body can be told apart from a genuine retry.
func HashRequest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Claim reserves (scope, key) in tx. It returns "" when this is the first
// use (the caller goes on to create, then calls Complete), or the result_ref
// a previous committed request recorded (the caller returns that document).
// A concurrent first request holding the same key makes this wait for it to
// commit or roll back, so two in-flight duplicates never both create.
func Claim(ctx context.Context, tx Querier, scope, key, requestHash string) (string, error) {
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO request_idempotency (scope, idem_key, request_hash)
		VALUES ($1, $2, $3)
		ON CONFLICT (scope, idem_key) DO NOTHING
		RETURNING id`, scope, key, requestHash).Scan(&id)
	if err == nil {
		return "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("claim idempotency key: %w", err)
	}

	var storedHash, resultRef string
	if err := tx.QueryRow(ctx, `
		SELECT request_hash, result_ref FROM request_idempotency
		WHERE scope = $1 AND idem_key = $2`, scope, key).Scan(&storedHash, &resultRef); err != nil {
		return "", fmt.Errorf("read idempotency key: %w", err)
	}
	if storedHash != requestHash {
		return "", ErrKeyReuse
	}
	if resultRef == "" {
		return "", ErrInProgress
	}
	return resultRef, nil
}

// RowQuerier is the read-only subset satisfied by both *pgxpool.Pool and pgx.Tx.
type RowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Lookup returns the result_ref a committed request recorded for (scope, key),
// or "" when the key is unused or has no recorded result yet (callers then
// proceed to Claim). A key stored with a different request hash is ErrKeyReuse.
func Lookup(ctx context.Context, q RowQuerier, scope, key, requestHash string) (string, error) {
	var storedHash, resultRef string
	err := q.QueryRow(ctx, `
		SELECT request_hash, result_ref FROM request_idempotency
		WHERE scope = $1 AND idem_key = $2`, scope, key).Scan(&storedHash, &resultRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("lookup idempotency key: %w", err)
	}
	if storedHash != requestHash {
		return "", ErrKeyReuse
	}
	return resultRef, nil
}

// Complete records the created document's id against a key Claim reserved.
// It must run in the same transaction as Claim and the create.
func Complete(ctx context.Context, tx Querier, scope, key, resultRef string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE request_idempotency SET result_ref = $3
		WHERE scope = $1 AND idem_key = $2`, scope, key, resultRef); err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	return nil
}

// Prune deletes ledger rows created before cutoff and returns how many.
func Prune(ctx context.Context, q Querier, cutoff time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM request_idempotency WHERE created_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune idempotency keys: %w", err)
	}
	return tag.RowsAffected(), nil
}
