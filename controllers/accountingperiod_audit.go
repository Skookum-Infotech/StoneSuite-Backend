// controllers/accountingperiod_audit.go — audit snapshots for audit_logs plus
// the per-period history endpoint. Split from accountingperiod.go for the
// 300-line file cap.
package controllers

import (
	"log"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/accountingperiod"
	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// periodSnapshot flattens a Period into the map recorded in audit_logs.
func periodSnapshot(p *accountingperiod.Period) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":             p.ID,
		"name":           p.Name,
		"fiscalYearName": p.FiscalYearName,
		"periodNumber":   p.Number,
		"start":          p.Start,
		"end":            p.End,
		"status":         p.Status,
		"isBasePeriod":   p.IsBasePeriod,
	}
}

// periodSnapshotByID finds a period in a pre-change listing so the audit row
// carries a real before-image. Returns nil when the listing could not be read,
// which is not worth failing the request over — the after-image still records
// what happened.
func periodSnapshotByID(periods []accountingperiod.Period, id string) map[string]any {
	for i := range periods {
		if periods[i].ID == id {
			return periodSnapshot(&periods[i])
		}
	}
	return nil
}

// fiscalYearSnapshot flattens a FiscalYear for audit_logs. The twelve periods
// are summarized by count rather than embedded: the per-period trail already
// lives in accounting_period_history, and inlining them would make every
// generate row enormous.
func fiscalYearSnapshot(fy *accountingperiod.FiscalYear) map[string]any {
	if fy == nil {
		return nil
	}
	return map[string]any{
		"id":          fy.ID,
		"name":        fy.Name,
		"start":       fy.Start,
		"end":         fy.End,
		"status":      fy.Status,
		"periodCount": len(fy.Periods),
	}
}

// auditPeriod records a calendar setup, year generation, close or reopen.
func auditPeriod(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, resourceID string, oldVal, newVal map[string]any) {
	if err := workflow.LogAuditFull(r.Context(), pool, "", action,
		string(authz.ResourceAccountingPeriod), resourceID, "accounting_period",
		oldVal, newVal, map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("accountingperiod: audit %s %s: %v", action, resourceID, err)
	}
}

// History GET /api/tenant/finance/accounting-periods/{uuid}/history
func (h *AccountingPeriodOps) History(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authPeriod(w, r, authz.ActionRead)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := accountingperiod.History(r.Context(), pool, r.PathValue("uuid"), limit)
	if err != nil {
		apFail(w, err, "Failed to load the accounting period history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "history": entries})
}
