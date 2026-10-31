package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/inventory"
	"stonesuite-backend/query"
	"stonesuite-backend/salesorder"
)

// soFail maps a store error to an HTTP response.
func soFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	var shortage *inventory.StockShortageError
	if errors.As(err, &shortage) {
		writeStockShortage(w, shortage)
		return
	}
	switch {
	case errors.Is(err, salesorder.ErrNotFound):
		fail(w, http.StatusNotFound, "Sales order not found.")
	case errors.Is(err, salesorder.ErrActivityNotFound):
		fail(w, http.StatusNotFound, "Schedule activity not found.")
	case errors.Is(err, salesorder.ErrInvalidTransition),
		errors.Is(err, salesorder.ErrApprovalRequired),
		errors.Is(err, salesorder.ErrApprovalNotRequired),
		errors.Is(err, salesorder.ErrHasDependents):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, salesorder.ErrVersionConflict):
		fail(w, http.StatusConflict, "This sales order was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, salesorder.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case salesorder.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
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
