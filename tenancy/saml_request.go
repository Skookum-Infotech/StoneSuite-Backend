package tenancy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// samlRequestPruneAge is the cutoff CreateSAMLRequestState uses to
// opportunistically delete old saml_requests rows, as a lightweight
// substitute for a dedicated cleanup job.
const samlRequestPruneAge = time.Hour

// ErrSAMLRequestNotFound covers a missing, already-consumed, or expired SAML
// request state. All three cases are indistinguishable to the caller
// deliberately -- returning different errors would leak timing/existence
// information to anyone probing the ACS endpoint with guessed request ids.
var ErrSAMLRequestNotFound = errors.New("saml request not found")

// CreateSAMLRequestState inserts a short-lived row recording that tenantID
// initiated a SAML AuthnRequest with this id, for provider. The ACS callback
// uses this to resolve which tenant an inbound assertion belongs to (SAML
// responses carry no tenant context of their own) and to enforce single-use.
// Opportunistically deletes rows older than 1 hour first, as a lightweight
// substitute for a dedicated cleanup job.
func (c *ControlPlane) CreateSAMLRequestState(ctx context.Context, requestID, tenantID, provider string, ttl time.Duration) error {
	if _, err := c.pool.Exec(ctx,
		`DELETE FROM saml_requests WHERE created_at < $1`,
		time.Now().Add(-samlRequestPruneAge)); err != nil {
		return fmt.Errorf("prune expired saml request state: %w", err)
	}

	if _, err := c.pool.Exec(ctx,
		`INSERT INTO saml_requests (id, tenant_id, provider, expires_at) VALUES ($1, $2, $3, $4)`,
		requestID, tenantID, provider, time.Now().Add(ttl)); err != nil {
		return fmt.Errorf("create saml request state: %w", err)
	}
	return nil
}

// ConsumeSAMLRequestState validates and deletes a SAML request state row in
// one atomic statement (DELETE ... RETURNING), returning the tenant id it was
// created for. Returns ErrSAMLRequestNotFound if the id doesn't exist, was
// already consumed by a prior call, or has expired.
func (c *ControlPlane) ConsumeSAMLRequestState(ctx context.Context, requestID, provider string) (tenantID string, err error) {
	err = c.pool.QueryRow(ctx,
		`DELETE FROM saml_requests WHERE id = $1 AND provider = $2 AND expires_at > NOW() RETURNING tenant_id`,
		requestID, provider,
	).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrSAMLRequestNotFound
	}
	if err != nil {
		return "", fmt.Errorf("consume saml request state: %w", err)
	}
	return tenantID, nil
}
