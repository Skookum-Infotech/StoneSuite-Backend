package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/estimate"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/query"
)

// estimateFail maps an estimate-module error to its HTTP status and message.
func estimateFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, estimate.ErrNotFound):
		fail(w, http.StatusNotFound, "Estimate not found.")
	case errors.Is(err, estimate.ErrInvalidTransition),
		errors.Is(err, estimate.ErrApprovalRequired),
		errors.Is(err, estimate.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, estimate.ErrVersionConflict):
		fail(w, http.StatusConflict, "This estimate was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, estimate.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case estimate.IsClientError(err):
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
