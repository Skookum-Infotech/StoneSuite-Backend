// controllers/accountingperiod_locks.go -- the six granular sub-ledger lock
// endpoints: AP, AR, GL, each with a close (lock) and reopen (unlock) side.
// Split from accountingperiod_actions.go for the 300-line file cap, mirroring
// how accountingperiod_audit.go is already split out.
//
// Same authority tier as whole-period Close/Reopen (accounting_period:update)
// -- locking one sub-ledger is the same kind of act "closing the books"
// already requires, just scoped narrower. See design spec §2.1/§7.
package controllers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/accountingperiod"
	"stonesuite-backend/authz"
)

// LockAP POST /api/tenant/finance/accounting-periods/lock-ap
func (h *AccountingPeriodOps) LockAP(w http.ResponseWriter, r *http.Request) {
	h.changeLock(w, r, "ap_lock", accountingperiod.LockAPPeriods)
}

// UnlockAP POST /api/tenant/finance/accounting-periods/unlock-ap
func (h *AccountingPeriodOps) UnlockAP(w http.ResponseWriter, r *http.Request) {
	h.changeLock(w, r, "ap_unlock", accountingperiod.UnlockAPPeriods)
}

// LockAR POST /api/tenant/finance/accounting-periods/lock-ar
func (h *AccountingPeriodOps) LockAR(w http.ResponseWriter, r *http.Request) {
	h.changeLock(w, r, "ar_lock", accountingperiod.LockARPeriods)
}

// UnlockAR POST /api/tenant/finance/accounting-periods/unlock-ar
func (h *AccountingPeriodOps) UnlockAR(w http.ResponseWriter, r *http.Request) {
	h.changeLock(w, r, "ar_unlock", accountingperiod.UnlockARPeriods)
}

// LockGL POST /api/tenant/finance/accounting-periods/lock-gl
//
// This is the GL choke point journal.CheckPeriodOpen reads (see design spec
// §2.2) -- the only one of the six that actually gates anything today; AP/AR
// are modeled, settable, and audited, but nothing consumes them yet.
func (h *AccountingPeriodOps) LockGL(w http.ResponseWriter, r *http.Request) {
	h.changeLock(w, r, "gl_lock", accountingperiod.LockGLPeriods)
}

// UnlockGL POST /api/tenant/finance/accounting-periods/unlock-gl
func (h *AccountingPeriodOps) UnlockGL(w http.ResponseWriter, r *http.Request) {
	h.changeLock(w, r, "gl_unlock", accountingperiod.UnlockGLPeriods)
}

// changeLock is the shared body of all six lock/unlock handlers above --
// same request/response shape as Close/Reopen in accountingperiod_actions.go
// ({"periodIds":[...],"note":"..."} in, {"periods":[...],"booksClosedThrough":...}
// out), differing only in which store function applies the change and which
// history action verb the audit row carries.
func (h *AccountingPeriodOps) changeLock(w http.ResponseWriter, r *http.Request, auditAction string,
	apply func(ctx context.Context, pool *pgxpool.Pool, ids []string, note string, employeeID int) (*accountingperiod.StatusChangeResult, error)) {

	pool, identityID, ok := h.authPeriod(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in accountingperiod.StatusChangeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if len(in.PeriodIDs) == 0 {
		fail(w, http.StatusBadRequest, "periodIds must contain at least one period.")
		return
	}

	empID := resolveEmployeeID(r, identityID)
	before, _ := accountingperiod.List(r.Context(), pool, accountingperiod.Filters{})

	result, err := apply(r.Context(), pool, in.PeriodIDs, in.Note, empID)
	if err != nil {
		apFail(w, err, "Failed to update the accounting period lock.")
		return
	}

	for _, p := range result.Periods {
		auditPeriod(r, pool, empID, auditAction, p.ID,
			periodSnapshotByID(before, p.ID), periodSnapshot(&p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":            true,
		"periods":            result.Periods,
		"booksClosedThrough": result.BooksClosedThrough,
	})
}
