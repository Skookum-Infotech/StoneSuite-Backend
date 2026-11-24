// controllers/employees_support.go — error mapping and audit trail for EmployeeOps.
package controllers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/dberr"
	"stonesuite-backend/employee"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// employeeFail maps a store error to an HTTP response. Unexpected errors are a
// generic 500: the detail goes to the logs, never to the client.
func employeeFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, employee.ErrNotFound):
		fail(w, http.StatusNotFound, "Employee not found.")
	case errors.Is(err, employee.ErrInvalidTransition),
		errors.Is(err, employee.ErrApprovalRequired),
		errors.Is(err, employee.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, employee.ErrVersionConflict):
		fail(w, http.StatusConflict, "This employee was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, employee.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	default:
		var ce employee.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		if status, msg, ok := dberr.Map(err); ok {
			fail(w, status, msg)
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// employeeSnapshot flattens an employee into a JSON-able map for the audit trail.
func employeeSnapshot(e *employee.Employee) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"id":             e.ID,
		"number":         e.Number,
		"name":           e.DisplayName,
		"email":          e.Email,
		"status":         e.Status,
		"approvalStatus": e.ApprovalStatus,
		"vendorId":       e.VendorID,
	}
}

// auditEmployee records an employee mutation in the unified audit_logs table.
// Best-effort: a failure is logged, never returned -- the write already
// committed.
func auditEmployee(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldE, newE *employee.Employee) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "employee", recordID, "employee",
		employeeSnapshot(oldE), employeeSnapshot(newE), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		slog.WarnContext(ctx, "employee: audit write failed", "action", action, "recordId", recordID, "error", err)
	}
}
