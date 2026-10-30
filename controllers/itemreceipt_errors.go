package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/query"
)

// irFail maps a store error to an HTTP response.
func irFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, itemreceipt.ErrNotFound):
		fail(w, http.StatusNotFound, "Item receipt not found.")
	case errors.Is(err, itemreceipt.ErrInvalidTransition),
		errors.Is(err, itemreceipt.ErrAlreadyPosted),
		errors.Is(err, itemreceipt.ErrPONotReceivable),
		errors.Is(err, itemreceipt.ErrMovementAlreadyApplied):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, itemreceipt.ErrVersionConflict):
		fail(w, http.StatusConflict, "This item receipt was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case errors.Is(err, itemreceipt.ErrOverReceipt):
		// 403, not 400: the request is well-formed and the quantities are real.
		// The caller simply is not permitted to accept an over-delivery.
		fail(w, http.StatusForbidden, err.Error())
	case itemreceipt.IsClientError(err):
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
