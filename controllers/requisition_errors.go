package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/query"
	"stonesuite-backend/requisition"
)

// reqnFail maps a store error to an HTTP response.
func reqnFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, requisition.ErrNotFound):
		fail(w, http.StatusNotFound, "Requisition not found.")
	case errors.Is(err, requisition.ErrInvalidTransition),
		errors.Is(err, requisition.ErrApprovalRequired),
		errors.Is(err, requisition.ErrApprovalNotRequired),
		errors.Is(err, requisition.ErrHasDependents):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, requisition.ErrVersionConflict):
		fail(w, http.StatusConflict, "This requisition was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, requisition.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case requisition.IsClientError(err):
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
