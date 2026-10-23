package proposedaction

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// actionColumns is the shared SELECT / RETURNING column list for scanAction.
const actionColumns = `id, identity_id, user_id, kind, resource, payload, preview, source, status,
	result_ref, error_message, expires_at, created_at, resolved_at`

// Store persists and resolves proposed actions in the tenant database.
type Store struct {
	pool *pgxpool.Pool
	reg  *Registry
	ttl  time.Duration
}

// NewStore builds a Store; ttl is how long a proposal stays pending.
func NewStore(pool *pgxpool.Pool, reg *Registry, ttl time.Duration) *Store {
	return &Store{pool: pool, reg: reg, ttl: ttl}
}

// scanAction reads one actionColumns row.
func scanAction(row pgx.Row) (Action, error) {
	var (
		a       Action
		kind    string
		source  string
		status  string
		preview []byte
		ref     []byte
		errMsg  *string
	)
	if err := row.Scan(&a.ID, &a.IdentityID, &a.UserID, &kind, &a.Resource, &a.Payload, &preview,
		&source, &status, &ref, &errMsg, &a.ExpiresAt, &a.CreatedAt, &a.ResolvedAt); err != nil {
		return Action{}, err
	}
	a.Kind, a.Source, a.Status = Kind(kind), Source(source), Status(status)
	if err := json.Unmarshal(preview, &a.Preview); err != nil {
		return Action{}, fmt.Errorf("decode preview: %w", err)
	}
	if len(ref) > 0 {
		var r ResultRef
		if err := json.Unmarshal(ref, &r); err != nil {
			return Action{}, fmt.Errorf("decode result ref: %w", err)
		}
		a.ResultRef = &r
	}
	if errMsg != nil {
		a.ErrorMessage = *errMsg
	}
	return a, nil
}
