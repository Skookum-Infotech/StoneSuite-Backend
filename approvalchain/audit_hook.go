package approvalchain

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Audit actions passed to ModuleConfig.AuditTx.
const (
	auditActionApprove = "approve"
	auditActionReject  = "reject"
)

// runAuditTx invokes the module's AuditTx hook (nil = no-op) inside the
// Approve/Reject transaction; an error aborts the approval.
func runAuditTx(ctx context.Context, tx pgx.Tx, cfg ModuleConfig, internalID int, uuid, action string) error {
	if cfg.AuditTx == nil {
		return nil
	}
	if err := cfg.AuditTx(ctx, tx, internalID, uuid, action); err != nil {
		return fmt.Errorf("audit %s %s: %w", action, cfg.Record.Table, err)
	}
	return nil
}
