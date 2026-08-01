package controllers

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/accountingperiod"
	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// AccountingPeriodOps handles the Finance fiscal-calendar endpoints.
//
// Like ChartOfAccountsOps and InventoryOps, the fiscal calendar is shared
// tenant-global reference data rather than an owner-scoped record, so there is
// no per-record IDOR scope check beyond the resource-level
// accounting_period:<action> permission. This is deliberate, not an omission:
// accounting_period has no owner column to scope against, and a period is by
// definition the same period for every user in the tenant.
//
// Routes (all under /api/tenant/finance):
//
//	GET  /accounting-calendar                  — fiscal calendar config
//	POST /accounting-calendar/setup            — one-time base period setup
//	GET  /fiscal-years                         — list generated years
//	POST /fiscal-years                         — generate the next year
//	GET  /accounting-periods                   — list (?fiscalYear=&status=)
//	GET  /accounting-periods/current           — the period covering today
//	GET  /accounting-periods/{uuid}            — get
//	GET  /accounting-periods/{uuid}/history    — audit trail
//	POST /accounting-periods/close             — close one or many
//	POST /accounting-periods/reopen            — reopen one or many
//	POST /accounting-periods/lock-ap           — lock AP sub-ledger, one or many
//	POST /accounting-periods/unlock-ap         — unlock AP sub-ledger, one or many
//	POST /accounting-periods/lock-ar           — lock AR sub-ledger, one or many
//	POST /accounting-periods/unlock-ar         — unlock AR sub-ledger, one or many
//	POST /accounting-periods/lock-gl           — lock GL sub-ledger, one or many
//	POST /accounting-periods/unlock-gl         — unlock GL sub-ledger, one or many
type AccountingPeriodOps struct{}

// NewAccountingPeriodOps constructs the handler group.
func NewAccountingPeriodOps() *AccountingPeriodOps { return &AccountingPeriodOps{} }

// authPeriod resolves JWT + tenant pool + the accounting_period:<action> grant.
func (h *AccountingPeriodOps) authPeriod(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceAccountingPeriod, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID,
			"resource", string(authz.ResourceAccountingPeriod), "action", string(action))
		fail(w, http.StatusForbidden,
			"You do not have permission to "+string(action)+" accounting periods.")
		return nil, "", false
	}
	return pool, payload.ID, true
}

// apFail maps a store error to an HTTP response.
func apFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, accountingperiod.ErrNotFound):
		fail(w, http.StatusNotFound, "Accounting period not found.")
	case errors.Is(err, accountingperiod.ErrNotConfigured):
		fail(w, http.StatusConflict,
			"The fiscal calendar has not been set up yet. Configure the base period first.")
	case accountingperiod.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	case accountingperiod.IsConflict(err):
		fail(w, http.StatusConflict, err.Error())
	default:
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// Calendar GET /api/tenant/finance/accounting-calendar
func (h *AccountingPeriodOps) Calendar(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authPeriod(w, r, authz.ActionRead)
	if !ok {
		return
	}
	cal, err := accountingperiod.GetCalendar(r.Context(), pool)
	if err != nil {
		apFail(w, err, "Failed to load the fiscal calendar.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "calendar": cal})
}

// List GET /api/tenant/finance/accounting-periods?fiscalYear=FY2026&status=open
func (h *AccountingPeriodOps) List(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authPeriod(w, r, authz.ActionRead)
	if !ok {
		return
	}
	periods, err := accountingperiod.List(r.Context(), pool, accountingperiod.Filters{
		FiscalYear: r.URL.Query().Get("fiscalYear"),
		Status:     r.URL.Query().Get("status"),
	})
	if err != nil {
		apFail(w, err, "Failed to list accounting periods.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "periods": periods})
}

// Get GET /api/tenant/finance/accounting-periods/{uuid}
func (h *AccountingPeriodOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authPeriod(w, r, authz.ActionRead)
	if !ok {
		return
	}
	period, err := accountingperiod.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		apFail(w, err, "Failed to load the accounting period.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "period": period})
}

// Current GET /api/tenant/finance/accounting-periods/current
//
// Returns 200 with a null period rather than 404 when today falls outside the
// generated calendar: "no period covers today" is a normal state a tenant can
// be in (they have not generated next year yet), and the UI needs to render a
// prompt for it, not an error.
func (h *AccountingPeriodOps) Current(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authPeriod(w, r, authz.ActionRead)
	if !ok {
		return
	}
	period, err := accountingperiod.ForDate(r.Context(), pool, time.Now().UTC())
	if errors.Is(err, accountingperiod.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "period": nil})
		return
	}
	if err != nil {
		apFail(w, err, "Failed to load the current accounting period.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "period": period})
}

// FiscalYears GET /api/tenant/finance/fiscal-years
func (h *AccountingPeriodOps) FiscalYears(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authPeriod(w, r, authz.ActionRead)
	if !ok {
		return
	}
	years, err := accountingperiod.ListFiscalYears(r.Context(), pool)
	if err != nil {
		apFail(w, err, "Failed to list fiscal years.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "fiscalYears": years})
}
