package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/dberr"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/inventorytransfer"
	"stonesuite-backend/query"
)

// transferFail maps a store error to its status code.
func transferFail(w http.ResponseWriter, err error, fallback string) {
	var invalid *query.InvalidFilterError
	switch {
	case errors.Is(err, inventorytransfer.ErrNotFound):
		fail(w, http.StatusNotFound, "Transfer not found.")
	case errors.As(err, &invalid):
		fail(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, inventorytransfer.ErrNotEditable):
		fail(w, http.StatusConflict, "This transfer can no longer be edited.")
	case errors.Is(err, inventorytransfer.ErrVersionConflict):
		fail(w, http.StatusConflict, "This transfer was changed by someone else — reload and try again")
	case errors.Is(err, idempotency.ErrKeyReuse):
		fail(w, http.StatusConflict, "Idempotency-Key was already used with a different request")
	case errors.Is(err, idempotency.ErrInProgress):
		fail(w, http.StatusConflict, "A request with this Idempotency-Key is still in progress")
	case inventorytransfer.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		if status, msg, ok := dberr.Map(err); ok {
			fail(w, status, msg)
			return
		}
		fail(w, http.StatusInternalServerError, fallback)
	}
}
