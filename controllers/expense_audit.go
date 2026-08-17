// controllers/expense_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/expense"
	"stonesuite-backend/workflow"
)

// expSnapshot flattens an expense claim into a JSON-able map for the audit
// trail, mirroring reqnSnapshot for the Expense shape.
func expSnapshot(e *expense.Expense) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"id":            e.ID,
		"expenseNumber": e.Number,
		"status":        e.Status,
		"total":         e.Total,
		"claimantId":    e.ClaimantEmployeeID,
	}
}

// auditExp records an Expense mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditReqn.
func auditExp(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldExp, newExp *expense.Expense) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "expense", recordID, "expense",
		expSnapshot(oldExp), expSnapshot(newExp), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("expense: audit %s %s: %v", action, recordID, err)
	}
}

// auditExpDelete is the delete-specific variant, mirroring auditReqnDelete.
func auditExpDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldExp *expense.Expense) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "expense", recordID, "expense",
		expSnapshot(oldExp), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("expense: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/expenses/{uuid}/audit
// Returns the unified audit trail for a single expense claim (most recent first).
func (h *ExpenseOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authExpByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := loadAuditEntries(r.Context(), pool, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "recordId": id, "audit": entries,
	})
}
