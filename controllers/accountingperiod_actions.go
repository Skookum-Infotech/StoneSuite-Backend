// controllers/accountingperiod_actions.go — the mutating half: one-time base
// period setup, fiscal year generation, and close/reopen. Split from
// accountingperiod.go for the 300-line file cap.
package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/accountingperiod"
	"stonesuite-backend/authz"
)

// Setup POST /api/tenant/finance/accounting-calendar/setup
//
// Requires accounting_period:configure, which is separable from the close
// permission: setting up the calendar is a one-time administrative act, while
// closing the books is a recurring one with different authority.
func (h *AccountingPeriodOps) Setup(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authPeriod(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var in accountingperiod.SetupInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	fy, err := accountingperiod.Setup(r.Context(), pool, in, empID)
	if err != nil {
		apFail(w, err, "Failed to set up the fiscal calendar.")
		return
	}
	auditPeriod(r, pool, empID, "calendar_setup", fy.ID, nil, fiscalYearSnapshot(fy))
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "fiscalYear": fy})
}

// GenerateYear POST /api/tenant/finance/fiscal-years
func (h *AccountingPeriodOps) GenerateYear(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authPeriod(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in accountingperiod.GenerateInput
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	empID := resolveEmployeeID(r, identityID)
	result, err := accountingperiod.GenerateFiscalYear(r.Context(), pool, in, empID)
	if err != nil {
		apFail(w, err, "Failed to generate the fiscal year(s).")
		return
	}
	for i := range result.FiscalYears {
		fy := result.FiscalYears[i]
		auditPeriod(r, pool, empID, "generate_fiscal_year", fy.ID, nil, fiscalYearSnapshot(&fy))
	}
	resp := map[string]any{
		"success":              true,
		"fiscalYears":          result.FiscalYears,
		"fiscalYearStartMonth": result.FiscalYearStartMonth,
		"fiscalYearEndMonth":   result.FiscalYearEndMonth,
	}
	if len(result.FiscalYears) == 1 {
		resp["fiscalYear"] = result.FiscalYears[0]
	}
	writeJSON(w, http.StatusCreated, resp)
}

// Close POST /api/tenant/finance/accounting-periods/close
// Body: {"periodIds":["..."],"note":"..."}
//
// One endpoint serves both the single and the multiple case: a one-element
// list is the single close. Keeping them on one path is what stops the
// sequencing rules from drifting apart between them.
func (h *AccountingPeriodOps) Close(w http.ResponseWriter, r *http.Request) {
	h.changeStatus(w, r, true)
}

// Reopen POST /api/tenant/finance/accounting-periods/reopen
func (h *AccountingPeriodOps) Reopen(w http.ResponseWriter, r *http.Request) {
	h.changeStatus(w, r, false)
}

// changeStatus is the shared body of Close and Reopen.
func (h *AccountingPeriodOps) changeStatus(w http.ResponseWriter, r *http.Request, closing bool) {
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

	var (
		result *accountingperiod.StatusChangeResult
		err    error
		action = "reopen"
		verb   = "reopen"
	)
	if closing {
		action, verb = "close", "close"
		result, err = accountingperiod.Close(r.Context(), pool, in.PeriodIDs, in.Note, empID)
	} else {
		result, err = accountingperiod.Reopen(r.Context(), pool, in.PeriodIDs, in.Note, empID)
	}
	if err != nil {
		apFail(w, err, "Failed to "+verb+" the accounting period.")
		return
	}

	// One audit row per period, so the tenant audit browser can be filtered to
	// a single period's history the same way it is for any other resource.
	for _, p := range result.Periods {
		auditPeriod(r, pool, empID, action, p.ID,
			periodSnapshotByID(before, p.ID), periodSnapshot(&p))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":            true,
		"periods":            result.Periods,
		"booksClosedThrough": result.BooksClosedThrough,
	})
}
