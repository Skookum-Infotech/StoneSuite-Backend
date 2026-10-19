package recordemail

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAddressNotFound means no record owns the given token.
var ErrAddressNotFound = errors.New("reply address not found")

// Target is the record a reply address belongs to.
type Target struct {
	TenantID    string
	RecordID    string
	WorkflowKey string
}

// AddressStore reads and writes the control-plane record_email_address table.
type AddressStore struct {
	pool *pgxpool.Pool
}

// NewAddressStore wraps the control-plane pool.
func NewAddressStore(pool *pgxpool.Pool) *AddressStore { return &AddressStore{pool: pool} }

// TokenFor returns the record's address token, minting one on first use. The
// token is stable: every later mail about the record reuses it, so a customer
// replying to an old email still lands on the right record.
func (s *AddressStore) TokenFor(ctx context.Context, tenantID, recordID, workflowKey string) (string, error) {
	var token string
	err := s.pool.QueryRow(ctx,
		`SELECT token FROM record_email_address WHERE tenant_id = $1 AND record_id = $2`,
		tenantID, recordID).Scan(&token)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("look up reply address for record %s: %w", recordID, err)
	}

	fresh, err := NewToken()
	if err != nil {
		return "", err
	}
	// A concurrent first send for the same record loses the unique race here:
	// DO UPDATE is a no-op write that lets RETURNING hand back the winner's token.
	err = s.pool.QueryRow(ctx, `
		INSERT INTO record_email_address (token, tenant_id, record_id, workflow_key)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tenant_id, record_id) DO UPDATE SET workflow_key = EXCLUDED.workflow_key
		RETURNING token`, fresh, tenantID, recordID, workflowKey).Scan(&token)
	if err != nil {
		return "", fmt.Errorf("create reply address for record %s: %w", recordID, err)
	}
	return token, nil
}

// Resolve maps a token back to its tenant and record.
func (s *AddressStore) Resolve(ctx context.Context, token string) (Target, error) {
	var t Target
	err := s.pool.QueryRow(ctx,
		`SELECT tenant_id::text, record_id::text, workflow_key FROM record_email_address WHERE token = $1`,
		token).Scan(&t.TenantID, &t.RecordID, &t.WorkflowKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Target{}, ErrAddressNotFound
	}
	if err != nil {
		return Target{}, fmt.Errorf("resolve reply address: %w", err)
	}
	return t, nil
}
