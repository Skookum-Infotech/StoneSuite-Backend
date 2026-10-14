// controllers/bin_transfers_audit.go
package controllers

import (
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/bintransfer"
	"stonesuite-backend/workflow"
)

// btSnapshot flattens a bin transfer into a JSON-able map for the audit trail.
func btSnapshot(bt *bintransfer.BinTransfer) map[string]any {
	if bt == nil {
		return nil
	}
	return map[string]any{
		"id":             bt.ID,
		"number":         bt.Number,
		"status":         bt.StatusName,
		"approvalStatus": bt.ApprovalStatus,
		"location":       bt.WarehouseName,
		"slabCount":      bt.SlabCount,
		"totalArea":      bt.TotalArea,
	}
}

// auditBT records a bin transfer mutation in the unified audit_logs table.
// Best-effort: a failure is logged, never returned -- the write already
// committed, and the same audit write is what re-indexes the record for search.
func auditBT(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldBT, newBT *bintransfer.BinTransfer) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "bin_transfer", recordID, "bin_transfer",
		btSnapshot(oldBT), btSnapshot(newBT), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		slog.WarnContext(ctx, "bintransfer: audit write failed", "action", action, "recordId", recordID, "error", err)
	}
}

// auditBTDelete is the delete-specific variant: there is no "after".
func auditBTDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldBT *bintransfer.BinTransfer) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "bin_transfer", recordID, "bin_transfer",
		btSnapshot(oldBT), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		slog.WarnContext(ctx, "bintransfer: audit delete failed", "recordId", recordID, "error", err)
	}
}
