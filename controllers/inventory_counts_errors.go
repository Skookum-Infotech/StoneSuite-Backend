package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/inventorycount"
	"stonesuite-backend/query"
)

// countFail maps a store error to its status code.
func countFail(w http.ResponseWriter, err error, fallback string) {
	var invalid *query.InvalidFilterError
	switch {
	case errors.Is(err, inventorycount.ErrNotFound):
		fail(w, http.StatusNotFound, "Count not found.")
	case errors.As(err, &invalid):
		fail(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, inventorycount.ErrNotEditable):
		fail(w, http.StatusConflict, "This count can no longer be edited.")
	case errors.Is(err, inventorycount.ErrVersionConflict):
		fail(w, http.StatusConflict, "This count was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case inventorycount.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		if status, msg, ok := dberr.Map(err); ok {
			fail(w, status, msg)
			return
		}
		fail(w, http.StatusInternalServerError, fallback)
	}
}
