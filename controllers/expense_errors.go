// controllers/expense_errors.go
package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/expense"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/query"
)

// expFail maps a store error to an HTTP response.
func expFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, expense.ErrNotFound):
		fail(w, http.StatusNotFound, "Expense claim not found.")
	case errors.Is(err, expense.ErrInvalidTransition),
		errors.Is(err, expense.ErrApprovalRequired),
		errors.Is(err, expense.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, expense.ErrVersionConflict):
		fail(w, http.StatusConflict, "This expense claim was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, expense.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case expense.IsClientError(err):
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
