package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/bintransfer"
	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/query"
)

// binTransferFail maps a store error to an HTTP response. Unexpected errors are
// a generic 500: the detail goes to the logs, never to the client.
func binTransferFail(w http.ResponseWriter, err error, serverMsg string) {
	if status, ok := approvalRejectStatus(err); ok {
		fail(w, status, err.Error())
		return
	}
	switch {
	case errors.Is(err, bintransfer.ErrNotFound):
		fail(w, http.StatusNotFound, "Bin transfer not found.")
	case errors.Is(err, bintransfer.ErrInvalidTransition),
		errors.Is(err, bintransfer.ErrApprovalRequired),
		errors.Is(err, bintransfer.ErrApprovalNotRequired),
		errors.Is(err, bintransfer.ErrNotEditable):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, bintransfer.ErrVersionConflict):
		fail(w, http.StatusConflict, "This bin transfer was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, bintransfer.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	default:
		var ce bintransfer.ClientError
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
