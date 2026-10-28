package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/inventoryadjustment"
	"stonesuite-backend/query"
)

// adjustmentFail maps a store error to its status code.
func adjustmentFail(w http.ResponseWriter, err error, fallback string) {
	var invalid *query.InvalidFilterError
	switch {
	case errors.Is(err, inventoryadjustment.ErrNotFound):
		fail(w, http.StatusNotFound, "Adjustment not found.")
	case errors.As(err, &invalid):
		// A bad filter key is the caller's mistake, never a 500.
		fail(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, inventoryadjustment.ErrNotEditable):
		fail(w, http.StatusConflict, "This adjustment can no longer be edited.")
	case errors.Is(err, inventoryadjustment.ErrVersionConflict):
		fail(w, http.StatusConflict, "This adjustment was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case inventoryadjustment.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		if status, msg, ok := dberr.Map(err); ok {
			fail(w, status, msg)
			return
		}
		fail(w, http.StatusInternalServerError, fallback)
	}
}
