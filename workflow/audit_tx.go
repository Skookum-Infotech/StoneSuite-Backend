package workflow

import (
	"context"
	"fmt"
	"net"

	"github.com/jackc/pgx/v5"
)

// AuditRequest carries the HTTP request provenance a transactional audit row
// needs (who, from where). Controllers attach it to the context handed to a
// store call; the store then writes the audit row inside its own transaction.
type AuditRequest struct {
	ActorUserID string
	IPAddress   string
	SessionID   string
	AppVersion  string
}

type auditRequestKey struct{}

// WithAuditRequest returns a context carrying req for LogAuditTx.
func WithAuditRequest(ctx context.Context, req AuditRequest) context.Context {
	return context.WithValue(ctx, auditRequestKey{}, req)
}

// AuditRequestFrom returns the AuditRequest attached by WithAuditRequest.
func AuditRequestFrom(ctx context.Context) (AuditRequest, bool) {
	req, ok := ctx.Value(auditRequestKey{}).(AuditRequest)
	return req, ok
}

// LogAuditTx writes one audit row inside tx so it commits or rolls back with
// the business write. It is a no-op when ctx carries no AuditRequest (non-HTTP
// callers such as automation audit themselves). Unlike LogAuditFull on a pool
// it is NOT best-effort: the error is returned and the caller must roll back.
func LogAuditTx(
	ctx context.Context, tx pgx.Tx,
	action, resource, resourceID, tableName string,
	oldVal, newVal, extraMeta map[string]any,
) error {
	req, ok := AuditRequestFrom(ctx)
	if !ok {
		return nil
	}
	if err := LogAuditFull(ctx, tx, req.ActorUserID, action, resource, resourceID, tableName,
		oldVal, newVal, extraMeta, req.IPAddress, req.SessionID, req.AppVersion); err != nil {
		return fmt.Errorf("log audit in tx: %w", err)
	}
	return nil
}

// auditIP returns ip when net.ParseIP accepts it, else "" (stored as NULL), so
// a malformed forwarded header can never fail an audited money write.
func auditIP(ip string) string {
	if net.ParseIP(ip) == nil {
		return ""
	}
	return ip
}
